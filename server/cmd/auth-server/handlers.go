package main

import (
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"lunar-tear/server/internal/auth"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed login.html
var loginFS embed.FS

var loginTmpl = template.Must(template.ParseFS(loginFS, "login.html"))

// oauthRedirectTmpl drives the fbconnect:// hand-off via a renderer-initiated
// navigation instead of a server-side 302. Android WebView does NOT invoke
// WebViewClient.shouldOverrideUrlLoading for 302 redirects from POST form
// submissions to non-http schemes (documented Chromium WebView limitation,
// Stack Overflow #6738328 / Google issuetracker #36918490). Returning a 200
// HTML page with both <meta http-equiv="refresh"> and window.location.replace()
// makes the cross-scheme navigation renderer-initiated, which DOES invoke
// shouldOverrideUrlLoading, so the FB SDK can extract access_token from the
// URL fragment and complete its login flow. html/template auto-escapes {{.}}
// correctly for the meta URL-attribute context and the JS string-literal
// context inside <script>.
var oauthRedirectTmpl = template.Must(template.New("oauthRedirect").Parse(
	`<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="refresh" content="0;url={{.}}">
</head><body>
<script>
var t = '{{.}}';
function post(m){try{if(window.uniwebview&&uniwebview.postMessage)uniwebview.postMessage(m);}catch(e){}try{if(window.uniwebview&&uniwebview.message)uniwebview.message(m);}catch(e){}}
try{
  var m = /[?&]url=([^&]*)/.exec(t);
  var inner = m ? decodeURIComponent(m[1]) : t;
  var q = t.substring(t.indexOf('?')+1);
  post('inquiry?'+q);
  post('inquiry?url='+encodeURIComponent(inner));
  post(inner);
  post(t);
  post('done'); post('close');
}catch(e){}
setTimeout(function(){window.location.replace(t);}, 150);
</script>
<noscript><a href="{{.}}">Continue</a></noscript>
</body></html>
`))

var transferDoneTmpl = template.Must(template.New("transferDone").Parse(
	`<!doctype html><html lang="ja"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>データ引き継ぎ</title>
<style>
:root{--bg-cream:#cfc7b0;--bg-cream-dark:#b8b099;--fg-dark:#2c2922;--fg-mid:#57523f;--accent:#1a1814;--panel:#d8d1bb;--success:#3a4d24}
*{box-sizing:border-box}
body{margin:0;background:var(--bg-cream);color:var(--fg-dark);font-family:"Segoe UI","Tahoma","Verdana",sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;padding:16px}
.card{background:var(--panel);border:2px solid var(--accent);box-shadow:6px 6px 0 var(--accent);max-width:420px;width:100%;padding:22px 20px;text-align:center}
.title{font-size:19px;letter-spacing:.14em;margin:0 0 14px;padding-bottom:10px;border-bottom:1px solid var(--accent)}
.badge{display:inline-block;background:var(--success);color:var(--bg-cream);font-size:12px;letter-spacing:.12em;padding:4px 12px;margin-bottom:12px}
.row{display:flex;justify-content:space-between;gap:12px;padding:7px 0;border-bottom:1px dashed var(--fg-mid);font-size:14px}
.row:last-child{border-bottom:none}
.row b{font-weight:600}
</style></head><body>
<div class="card">
  <h1 class="title">デ ー タ 引 継 ぎ</h1>
  <span class="badge">完 了</span>
  <div class="row"><span>アカウント</span><b>{{.User}}</b></div>
  <div class="row"><span>処理</span><b>{{.Action}}</b></div>
  <div class="row"><span>日時</span><b>{{.Time}}</b></div>
</div>
</body></html>
`))

type Handlers struct {
	store      *auth.AuthStore
	tok        *auth.TokenService
	noRegister bool
	gameDB     *sql.DB
}

func NewHandlers(store *auth.AuthStore, tok *auth.TokenService, noRegister bool, gameDB *sql.DB) *Handlers {
	if gameDB != nil {
		if _, err := gameDB.Exec(`CREATE TABLE IF NOT EXISTS bridge_links (
			auth_user_id INTEGER PRIMARY KEY,
			game_user_id INTEGER NOT NULL,
			linked_at TEXT NOT NULL
		)`); err != nil {
			log.Printf("[bridge] init bridge_links: %v", err)
		}
	}
	return &Handlers{store: store, tok: tok, noRegister: noRegister, gameDB: gameDB}
}

// ---- JP 引继：backup_token ↔ 游戏账号 ↔ SE 账号 绑定 ----

func (h *Handlers) gameUserByBackupToken(token string) (int64, string, error) {
	var uid int64
	var uu string
	err := h.gameDB.QueryRow(`SELECT user_id, uuid FROM users WHERE backup_token = ?`, token).Scan(&uid, &uu)
	return uid, uu, err
}

func (h *Handlers) linkAccount(authUserID, gameUserID int64) error {
	_, err := h.gameDB.Exec(
		`INSERT INTO bridge_links(auth_user_id, game_user_id, linked_at) VALUES(?,?,?)
		 ON CONFLICT(auth_user_id) DO UPDATE SET game_user_id=excluded.game_user_id, linked_at=excluded.linked_at`,
		authUserID, gameUserID, time.Now().Format(time.RFC3339))
	return err
}

func (h *Handlers) linkedGameUser(authUserID int64) (int64, error) {
	var gid int64
	err := h.gameDB.QueryRow(`SELECT game_user_id FROM bridge_links WHERE auth_user_id = ?`, authUserID).Scan(&gid)
	return gid, err
}

// rebindForTransfer：删除新设备的临时账号（tokenUser），把它的 uuid 移交给旧账号（targetUser）。
// 与 claim-account 工具同类逻辑，但自动找出所有带 user_id 列的子表。
func (h *Handlers) rebindForTransfer(tokenUserID int64, tokenUUID string, targetUserID int64) error {
	tx, err := h.gameDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()

	for _, t := range tables {
		cols, err := tx.Query(`PRAGMA table_info("` + t + `")`)
		if err != nil {
			continue
		}
		hasUser := false
		for cols.Next() {
			var cid, notnull, pk int
			var name, ctype string
			var dflt interface{}
			if err := cols.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				break
			}
			if name == "user_id" {
				hasUser = true
			}
		}
		cols.Close()
		if hasUser {
			if _, err := tx.Exec(`DELETE FROM "`+t+`" WHERE user_id = ?`, tokenUserID); err != nil {
				return fmt.Errorf("delete %s: %w", t, err)
			}
		}
	}

	if _, err := tx.Exec(`DELETE FROM users WHERE user_id = ?`, tokenUserID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE users SET uuid = ? WHERE user_id = ?`, tokenUUID, targetUserID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, targetUserID); err != nil {
		return err
	}
	return tx.Commit()
}

type loginPageData struct {
	RedirectURI string
	State       string
	Scope       string
	Error       string
	Username    string
}

func isOAuthPath(path string) bool {
	// Match /v{N}/dialog/oauth or /v{N}.{M}/dialog/oauth
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 3 {
		return false
	}
	return strings.HasPrefix(parts[0], "v") && parts[1] == "dialog" && parts[2] == "oauth"
}

func isMePath(path string) bool {
	p := strings.TrimPrefix(path, "/")
	if p == "me" {
		return true
	}
	parts := strings.Split(p, "/")
	return len(parts) == 2 && strings.HasPrefix(parts[0], "v") && parts[1] == "me"
}

// HandleBridge 处理 JP「データ引き継ぎ」/「引き継ぎ登録」的私服桥接页。
// 客户端被补丁为访问 http://<auth-host>/ntv/{gameId}/reg|update/top?type=..&token=..
// 登录/注册成功后尝试以 nierspjp:// 深链回调（app 在 manifest 中注册了该 scheme）。
func (h *Handlers) HandleBridge(w http.ResponseWriter, r *http.Request) {
	log.Printf("[bridge] %s %s?%s ua=%q", r.Method, r.URL.Path, r.URL.RawQuery, r.UserAgent())

	if r.Method == http.MethodPost {
		h.bridgePost(w, r)
		return
	}

	data := loginPageData{
		RedirectURI: "nierspjp://transfer",
		State:       r.URL.Query().Get("token"),
		Scope:       r.URL.Query().Get("type"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginTmpl.Execute(w, data); err != nil {
		log.Printf("render bridge page: %v", err)
	}
}

func (h *Handlers) bridgePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	action := r.FormValue("action")

	renderErr := func(msg string) {
		data := loginPageData{RedirectURI: "nierspjp://transfer", Error: msg, Username: username}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := loginTmpl.Execute(w, data); err != nil {
			log.Printf("render bridge page: %v", err)
		}
	}

	if username == "" || password == "" {
		renderErr("Username and password are required.")
		return
	}

	var user auth.AuthUser
	var err error
	switch action {
	case "register":
		if h.noRegister {
			renderErr("This server does not accept user registrations.")
			return
		}
		user, err = h.store.CreateUser(username, password)
		if err == auth.ErrUserExists {
			renderErr("Username is already taken.")
			return
		}
	case "login":
		user, err = h.store.VerifyUser(username, password)
		if err == auth.ErrInvalidCreds {
			renderErr("Invalid username or password.")
			return
		}
	default:
		renderErr("Invalid action.")
		return
	}
	if err != nil {
		log.Printf("[bridge] user error: %v", err)
		renderErr("Server error. Try again.")
		return
	}

	token, err := h.tok.Generate(user)
	if err != nil {
		renderErr("Server error. Try again.")
		return
	}

	// ---- JP 引继：绑定 / 改绑 ----
	bridgeToken := strings.TrimSpace(r.FormValue("state"))
	isUpdate := strings.Contains(r.URL.Path, "update")
	if h.gameDB != nil && bridgeToken != "" {
		gameUserID, gameUUID, err := h.gameUserByBackupToken(bridgeToken)
		if err != nil {
			log.Printf("[bridge] token %s: no game user (%v)", bridgeToken, err)
		} else if isUpdate {
			target, err := h.linkedGameUser(user.ID)
			if err != nil {
				log.Printf("[bridge] auth user %d has no linked game account: %v", user.ID, err)
			} else if target == gameUserID {
				log.Printf("[bridge] self-target, nothing to do (user %d)", target)
			} else if err := h.rebindForTransfer(gameUserID, gameUUID, target); err != nil {
				log.Printf("[bridge] rebind failed: %v", err)
				renderErr("Transfer failed.")
				return
			} else {
				log.Printf("[bridge] TRANSFERRED user %d -> uuid %s (temp user %d removed)", target, gameUUID, gameUserID)
			}
		} else {
			if err := h.linkAccount(user.ID, gameUserID); err != nil {
				log.Printf("[bridge] link failed: %v", err)
			} else {
				log.Printf("[bridge] LINKED auth user %d <-> game user %d (uuid %s)", user.ID, gameUserID, gameUUID)
			}
		}
	}

	payload := fmt.Sprintf(`{"user_id":"%d"}`, user.ID)
	b64 := base64.RawURLEncoding.EncodeToString([]byte(payload))
	frag := url.Values{}
	frag.Set("access_token", token)
	frag.Set("token_type", "bearer")
	frag.Set("signed_request", "0."+b64)
	inner := "nierspjp://bridge-done/inquiry?" + frag.Encode()
	target := "uniwebview://bridge-done/inquiry?url=" + url.QueryEscape(inner) + "&" + frag.Encode()
	log.Printf("[bridge] %s ok user=%q -> %s", action, user.Username, target)
	view := struct {
		User   string
		Action string
		Time   string
		Target string
	}{user.Username, action, time.Now().Format("2006-01-02 15:04"), target}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	if err := transferDoneTmpl.Execute(w, view); err != nil {
		log.Printf("render bridge redirect: %v", err)
	}
}

func (h *Handlers) HandleOAuth(w http.ResponseWriter, r *http.Request) {
	if isMePath(r.URL.Path) {
		h.HandleMe(w, r)
		return
	}

	if !isOAuthPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.oauthGet(w, r)
	case http.MethodPost:
		h.oauthPost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) oauthGet(w http.ResponseWriter, r *http.Request) {
	data := loginPageData{
		RedirectURI: r.URL.Query().Get("redirect_uri"),
		State:       r.URL.Query().Get("state"),
		Scope:       r.URL.Query().Get("scope"),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := loginTmpl.Execute(w, data); err != nil {
		log.Printf("render login page: %v", err)
	}
}

func (h *Handlers) oauthPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	action := r.FormValue("action")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	scope := r.FormValue("scope")

	renderErr := func(msg string) {
		data := loginPageData{
			RedirectURI: redirectURI,
			State:       state,
			Scope:       scope,
			Error:       msg,
			Username:    username,
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := loginTmpl.Execute(w, data); err != nil {
			log.Printf("render login page: %v", err)
		}
	}

	if username == "" || password == "" {
		renderErr("Username and password are required.")
		return
	}

	var user auth.AuthUser
	var err error

	switch action {
	case "register":
		if h.noRegister {
			renderErr("This server does not accept user registrations.")
			return
		}

		user, err = h.store.CreateUser(username, password)
		if err == auth.ErrUserExists {
			renderErr("Username is already taken.")
			return
		}
		if err != nil {
			log.Printf("create user: %v", err)
			renderErr("Server error. Try again.")
			return
		}
		log.Printf("registered user %q (id=%d)", user.Username, user.ID)

	case "login":
		user, err = h.store.VerifyUser(username, password)
		if err == auth.ErrInvalidCreds {
			renderErr("Invalid username or password.")
			return
		}
		if err != nil {
			log.Printf("verify user: %v", err)
			renderErr("Server error. Try again.")
			return
		}
		log.Printf("authenticated user %q (id=%d)", user.Username, user.ID)

	default:
		renderErr("Invalid action.")
		return
	}

	token, err := h.tok.Generate(user)
	if err != nil {
		log.Printf("generate token: %v", err)
		renderErr("Server error. Try again.")
		return
	}

	payload := fmt.Sprintf(`{"user_id":"%d"}`, user.ID)
	b64 := base64.RawURLEncoding.EncodeToString([]byte(payload))

	fragment := url.Values{}
	fragment.Set("access_token", token)
	fragment.Set("token_type", "bearer")
	fragment.Set("expires_in", strconv.FormatInt(int64(auth.TokenTTL.Seconds()), 10))
	fragment.Set("signed_request", "0."+b64)
	// iOS FBSDKLoginManager treats an empty granted_scopes set as a cancelled login
	// (LoginManager.swift -> getSuccessResult -> getCancelledResult). Echo back the
	// scope the SDK sent so parameters.permissions is non-empty and the SDK fires
	// its success path. Android tolerates either way.
	if scope != "" {
		fragment.Set("granted_scopes", scope)
		fragment.Set("denied_scopes", "")
	}
	if state != "" {
		fragment.Set("state", state)
	}

	target := redirectURI + "?" + fragment.Encode()
	log.Printf("redirecting to %s", target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := oauthRedirectTmpl.Execute(w, target); err != nil {
		log.Printf("render oauth redirect: %v", err)
	}
}

func (h *Handlers) HandleCheckUsername(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	w.Header().Set("Content-Type", "application/json")
	if username == "" {
		json.NewEncoder(w).Encode(map[string]bool{"exists": false})
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"exists": h.store.UserExists(username)})
}

func (h *Handlers) HandleMe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("access_token")
	if token == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token = auth[7:]
		}
	}

	if token == "" {
		http.Error(w, `{"error":{"message":"missing access_token","type":"OAuthException","code":190}}`, http.StatusUnauthorized)
		return
	}

	claims, err := h.tok.Validate(token)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"OAuthException","code":190}}`, err), http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"id":   strconv.FormatInt(claims.Sub, 10),
		"name": claims.Name,
	})
}
