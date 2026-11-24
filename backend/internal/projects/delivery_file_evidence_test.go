package projects

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"alliance/internal/knowledge"
	"alliance/internal/resources"
)

func TestDeliveryFileProofIsBoundAndExpires(t *testing.T) {
	vault, err := resources.NewVault(strings.Repeat("a3", 32))
	if err != nil {
		t.Fatal(err)
	}
	s := Server{Vault: vault}
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	review := deliveryFileReview{"project", "member", "run", "artifact", 1, 2}
	evidence := deliveryFileEvidence{knowledge.Sentence{Text: "质保一年", Location: "条款!D2", Start: 11, End: 14}, "报价.xlsx", strings.Repeat("d", 64)}
	body, err := json.Marshal(deliveryFileProof{evidence, now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(string(body), review.scope(1))
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(sealed)
	if got, err := s.readDeliveryFileProof(token, review, 1, now); err != nil || got != evidence {
		t.Fatalf("current file selection was not opened: %+v %v", got, err)
	}
	if _, err := s.readDeliveryFileProof(token, review, 2, now); err == nil {
		t.Fatal("proof from a different plan was accepted")
	}
	if _, err := s.readDeliveryFileProof(token, review, 1, now.Add(time.Minute)); err == nil {
		t.Fatal("expired proof was accepted")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := s.readDeliveryFileProof(base64.RawURLEncoding.EncodeToString(sealed), review, 1, now); err == nil {
		t.Fatal("modified proof was accepted")
	}
}
