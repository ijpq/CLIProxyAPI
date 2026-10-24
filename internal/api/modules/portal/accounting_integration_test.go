package portal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/billing"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
)

type uncertainCommitSink struct{ store *store.PostgresStore }

func (s uncertainCommitSink) RecordUsageAndDebit(ctx context.Context, event store.MeteredUsage) error {
	if err := s.store.RecordUsageAndDebit(ctx, event); err != nil {
		return err
	}
	return errors.New("synthetic connection lost after commit")
}

func TestPortalAccountingReplayDoesNotDebitCommittedEventTwice(t *testing.T) {
	f := newPortalDBFixture(t)
	u, _ := f.register(t, "journal-recovery@example.test")
	ctx := context.Background()
	if _, err := f.store.AdminCreditWallet(ctx, u.ID, "10", "test", ""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	journal, err := billing.NewDurableMeterSink(uncertainCommitSink{store: f.store}, dir)
	if err != nil {
		t.Fatal(err)
	}
	event := store.MeteredUsage{EventID: "synthetic-child-event-1", UserID: u.ID, RequestID: "same-parent", Model: "test-model", Cost: "2", PriceVersion: 1}
	if err = journal.RecordUsageAndDebit(ctx, event); err == nil {
		t.Fatal("uncertain commit not returned")
	}
	restarted, err := billing.NewDurableMeterSink(f.store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Replay(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			if err := f.store.RecordUsageAndDebit(ctx, event); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var records, ledger int
	if err = f.db.QueryRow(`SELECT count(*) FROM "` + f.schema + `".usage_records`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(`SELECT count(*) FROM "` + f.schema + `".transactions WHERE type='consume'`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if records != 1 || ledger != 1 {
		t.Fatalf("event replay duplicated usage=%d ledger=%d", records, ledger)
	}
	event.EventID = "synthetic-child-event-2"
	if err = f.store.RecordUsageAndDebit(ctx, event); err != nil {
		t.Fatal(err)
	}
	if balance, err := f.store.GetWalletBalance(ctx, u.ID); err != nil || balance != "6.000000" {
		t.Fatalf("continuation charge=%s err=%v", balance, err)
	}
	event.Cost = "3"
	if err = f.store.RecordUsageAndDebit(ctx, event); err == nil {
		t.Fatal("conflicting event identity accepted")
	}
}

func TestPortalHistoricalMoneyIsNotRelabeledAsPoints(t *testing.T) {
	f := newPortalDBFixture(t)
	u, token := f.register(t, "legacy-history@example.test")
	ctx := context.Background()
	if err := f.store.RecordUsageAndDebit(ctx, store.MeteredUsage{UserID: u.ID, Model: "legacy-money-model", Cost: "1.5", SkipDebit: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE "`+f.schema+`".usage_records SET unit='LEGACY' WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "GET", "/usage", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"unit":"LEGACY"`) {
		t.Fatal("historical currency charge was mislabeled as points")
	}
	w = f.request(t, "GET", "/usage/stats", token, nil)
	requirePortalStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "legacy-money-model") {
		t.Fatal("legacy money was added to points statistics")
	}
	f.updateUser(t, u.ID, "active", true)
	if _, err := f.db.Exec(`UPDATE "`+f.schema+`".wallets SET unit='LEGACY' WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	w = f.request(t, "GET", "/admin/users", token, nil)
	requirePortalStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"balance_unit":"LEGACY"`) {
		t.Fatal("administrator list hid the reconciliation requirement")
	}
}

func TestPortalAdministratorBootstrapRequiresSelectedUserID(t *testing.T) {
	f := newPortalDBFixture(t)
	selected, _ := f.register(t, "approved-admin@example.test")
	other, _ := f.register(t, "unverified-admin-name@example.test")
	if err := f.store.PromoteUserIDToAdmin(context.Background(), selected.ID); err != nil {
		t.Fatal(err)
	}
	admin, _ := f.store.GetUserByID(context.Background(), selected.ID)
	unselected, _ := f.store.GetUserByID(context.Background(), other.ID)
	if !admin.IsAdmin || unselected.IsAdmin {
		t.Fatal("administrator bootstrap promoted an unselected account")
	}
	f.updateUser(t, other.ID, "suspended", false)
	if err := f.store.PromoteUserIDToAdmin(context.Background(), other.ID); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("suspended administrator bootstrap: %v", err)
	}
}
