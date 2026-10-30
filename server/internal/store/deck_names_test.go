package store

import (
	"testing"

	"lunar-tear/server/internal/model"
)

func TestDefaultDeckName(t *testing.T) {
	cases := map[int32]string{1: "Deck 1", 2: "Deck 2", 3: "Deck 3", 12: "Deck 12"}
	for n, want := range cases {
		if got := DefaultDeckName(n); got != want {
			t.Fatalf("DefaultDeckName(%d) = %q, want %q", n, got, want)
		}
	}
}

// The client lists the initial quest-deck slots (kInitialDeckListDeckNumbers)
// even when they have no row; each slot must be seeded with its "Deck N" name
// or the client falls back to its localized "type name + number" default.
func TestEnsureDefaultDeckSeedsInitialSlotsWithDefaultNames(t *testing.T) {
	user := &UserState{}
	user.EnsureMaps()
	user.Costumes["costume-1"] = CostumeState{UserCostumeUuid: "costume-1", CostumeId: 10100}
	user.Weapons["weapon-1"] = WeaponState{UserWeaponUuid: "weapon-1", WeaponId: 101001}

	EnsureDefaultDeck(user, 1000)

	if len(InitialDeckNumbers) != 3 {
		t.Fatalf("InitialDeckNumbers = %v, want 3 slots", InitialDeckNumbers)
	}
	for _, n := range InitialDeckNumbers {
		deck, ok := user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: n}]
		if !ok {
			t.Fatalf("missing quest deck slot %d", n)
		}
		if want := DefaultDeckName(n); deck.Name != want {
			t.Fatalf("slot %d name = %q, want %q", n, deck.Name, want)
		}
		if deck.DeckType != model.DeckTypeQuest || deck.UserDeckNumber != n {
			t.Fatalf("slot %d identity = (%d,%d), want (1,%d)", n, deck.DeckType, deck.UserDeckNumber, n)
		}
		if n == 1 {
			if deck.UserDeckCharacterUuid01 == "" {
				t.Fatal("starter deck slot 1 lost its deck character")
			}
		} else if deck.UserDeckCharacterUuid01 != "" || deck.UserDeckCharacterUuid02 != "" || deck.UserDeckCharacterUuid03 != "" {
			t.Fatalf("slot %d should start empty, got %q/%q/%q", n,
				deck.UserDeckCharacterUuid01, deck.UserDeckCharacterUuid02, deck.UserDeckCharacterUuid03)
		}
	}

	// Second call must not rewrite anything (guard: decks already exist).
	user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}] = DeckState{
		DeckType: model.DeckTypeQuest, UserDeckNumber: 2, Name: "自定义名",
	}
	EnsureDefaultDeck(user, 2000)
	if got := user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}].Name; got != "自定义名" {
		t.Fatalf("EnsureDefaultDeck overwrote existing deck name: %q", got)
	}
}

func TestApplyDeckReplacementNameHandling(t *testing.T) {
	user := &UserState{}
	user.EnsureMaps()

	// New deck: empty name is filled with the "Deck N" default.
	ApplyDeckReplacement(user, model.DeckTypeQuest, 2, nil, 1000)
	deck := user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}]
	if deck.Name != "Deck 2" {
		t.Fatalf("new deck name = %q, want %q", deck.Name, "Deck 2")
	}

	// Existing custom name: never rewritten.
	deck.Name = "我的最爱"
	user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}] = deck
	ApplyDeckReplacement(user, model.DeckTypeQuest, 2, nil, 2000)
	if got := user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}].Name; got != "我的最爱" {
		t.Fatalf("custom name rewritten to %q", got)
	}

	// Empty name on replace: re-seeded with the default.
	deck.Name = ""
	user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}] = deck
	ApplyDeckReplacement(user, model.DeckTypeQuest, 2, nil, 3000)
	if got := user.Decks[DeckKey{DeckType: model.DeckTypeQuest, UserDeckNumber: 2}].Name; got != "Deck 2" {
		t.Fatalf("empty name not backfilled, got %q", got)
	}
}
