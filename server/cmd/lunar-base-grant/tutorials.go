package main

// Tutorial prep: write IUserTutorialProgress rows so the client believes the
// freeze-prone tutorials were already completed (they are never played), while
// the features they introduce still get unlocked. Quests, story progress,
// rewards and drops are left untouched.
//
// prep_tutorials  - write {type, phase, choice_id} rows (never downgrading),
//                   optionally create the default quest deck and grant the
//                   companion tutorial reward
// clear_tutorials - delete the given tutorial rows (revert a prep)

import (
	"errors"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/google/uuid"

	"lunar-tear/server/internal/model"
	"lunar-tear/server/internal/store"
)

// tutorialSpec is one tutorial progress row to write.
type tutorialSpec struct {
	Type     int32 `json:"type"`
	Phase    int32 `json:"phase"`
	ChoiceID int32 `json:"choice_id"`
}

// Emitted by main() for the tutorial actions.
var (
	queryTutorialTypes   []int32
	queryTutorialSkipped []int32
	queryDeckCreated     bool
	queryCompanionID     int32
)

func runPrepTutorials(req *request) (int, error) {
	if len(req.Tutorials) == 0 {
		return 0, errors.New("tutorials list is empty")
	}
	db, st, err := openDB(req.DBPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	// The companion reward goes through the same QuestHandler path the live
	// server uses, which needs the master data (like the quest actions).
	var env *questEnv
	if req.GrantCompanion {
		env, err = loadQuestEnv(req.MasterDataPath)
		if err != nil {
			return 0, err
		}
	}

	now := time.Now().UnixMilli()
	written := []int32{}
	skipped := []int32{}
	deckCreated := false
	companionID := int32(0)

	_, err = st.UpdateUser(req.UserID, func(u *store.UserState) {
		u.EnsureMaps()
		for _, t := range req.Tutorials {
			if t.Type <= 0 || t.Phase <= 0 {
				log.Printf("[prep_tutorials] ignoring invalid spec type=%d phase=%d", t.Type, t.Phase)
				continue
			}
			cur := u.Tutorials[t.Type]
			if cur.ProgressPhase >= t.Phase {
				// Already at (or past) the target: never downgrade.
				skipped = append(skipped, t.Type)
				continue
			}
			u.Tutorials[t.Type] = store.TutorialProgressState{
				TutorialType:  t.Type,
				ProgressPhase: t.Phase,
				ChoiceId:      t.ChoiceID,
				LatestVersion: now,
			}
			written = append(written, t.Type)
		}
		if req.CreateDeck {
			deckCreated = ensureStarterDeck(u, now)
		}
		if env != nil {
			for _, t := range req.Tutorials {
				if t.Type != int32(model.TutorialTypeCompanion) {
					continue
				}
				for _, g := range env.handler.ApplyTutorialReward(u, model.TutorialTypeCompanion, t.ChoiceID, now) {
					if g.PossessionType == model.PossessionTypeCompanion {
						companionID = g.PossessionId
					}
				}
			}
		}
	})
	if err != nil {
		return 0, fmt.Errorf("prep tutorials: %w", err)
	}
	log.Printf("[prep_tutorials] user=%d written=%v skipped=%v deck=%v companion=%d",
		req.UserID, written, skipped, deckCreated, companionID)

	queryTutorialTypes = written
	queryTutorialSkipped = skipped
	queryDeckCreated = deckCreated
	queryCompanionID = companionID
	return len(written), nil
}

// ensureStarterDeck mirrors store.EnsureDefaultDeck, but when the canonical
// starter weapon (101001, granted by chapter 2's first clear) is not owned
// yet it falls back to the lowest weapon id the account does own, so an early
// account still gets a usable deck. No items are granted.
func ensureStarterDeck(u *store.UserState, nowMillis int64) bool {
	if len(u.Costumes) == 0 || len(u.Decks) > 0 {
		return false
	}
	const rionCostumeId = int32(10100)
	const rionWeaponId = int32(101001)

	costumeUUID := ""
	for k, v := range u.Costumes {
		if v.CostumeId == rionCostumeId {
			costumeUUID = k
			break
		}
	}
	if costumeUUID == "" {
		keys := make([]string, 0, len(u.Costumes))
		for k := range u.Costumes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		costumeUUID = keys[0]
	}

	weaponUUID := ""
	bestID := int32(0)
	for k, v := range u.Weapons {
		if v.WeaponId == rionWeaponId {
			weaponUUID = k
			break
		}
		if bestID == 0 || v.WeaponId < bestID {
			bestID = v.WeaponId
			weaponUUID = k
		}
	}

	dcUUID := uuid.New().String()
	u.DeckCharacters[dcUUID] = store.DeckCharacterState{
		UserDeckCharacterUuid: dcUUID,
		UserCompanionUuid:     "",
		UserCostumeUuid:       costumeUUID,
		MainUserWeaponUuid:    weaponUUID,
		Power:                 100,
		LatestVersion:         nowMillis,
	}
	u.Decks[store.DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 1}] = store.DeckState{
		DeckType:                model.DeckTypeQuest,
		UserDeckNumber:          1,
		UserDeckCharacterUuid01: dcUUID,
		Name:                    "Deck 1",
		Power:                   100,
		LatestVersion:           nowMillis,
	}
	if _, exists := u.DeckTypeNotes[model.DeckTypeQuest]; !exists {
		u.DeckTypeNotes[model.DeckTypeQuest] = store.DeckTypeNoteState{
			DeckType:      model.DeckTypeQuest,
			MaxDeckPower:  100,
			LatestVersion: nowMillis,
		}
	}
	log.Printf("[prep_tutorials] created starter deck (costume=%s weapon=%s)", costumeUUID, weaponUUID)
	return true
}

func runClearTutorials(req *request) (int, error) {
	if len(req.TutorialTypes) == 0 {
		return 0, errors.New("tutorial_types list is empty")
	}
	db, st, err := openDB(req.DBPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()

	removed := []int32{}
	_, err = st.UpdateUser(req.UserID, func(u *store.UserState) {
		u.EnsureMaps()
		for _, t := range req.TutorialTypes {
			if _, exists := u.Tutorials[t]; exists {
				delete(u.Tutorials, t)
				removed = append(removed, t)
			}
		}
	})
	if err != nil {
		return 0, fmt.Errorf("clear tutorials: %w", err)
	}
	log.Printf("[clear_tutorials] user=%d removed=%v", req.UserID, removed)
	queryTutorialTypes = removed
	return len(removed), nil
}
