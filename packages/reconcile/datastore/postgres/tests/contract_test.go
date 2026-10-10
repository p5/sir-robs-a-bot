package postgrestests

import (
	"fmt"
	"testing"
	"time"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/postgres"
	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, contractFixture)
}

func TestInspectionContract(t *testing.T) {
	storetest.RunInspection(t, contractFixture)
}

func contractFixture(t *testing.T) storetest.Fixture {
	t.Helper()
	store, db, namespace := newFixture(t)
	return storetest.Fixture{
		Store:  store,
		Elapse: func(duration time.Duration) { advanceStoreTime(t, db, duration) },
		Reopen: func() datastore.Store {
			store, err := postgres.New(openTestPool(t), namespace)
			if err != nil {
				t.Fatalf("reopen store: %v", err)
			}
			return store
		},
	}
}

func TestOperationSequenceCorpus(t *testing.T) {
	for index, commands := range storetest.SequenceCorpus() {
		t.Run(fmt.Sprintf("history-%d", index), func(t *testing.T) {
			store, _, namespace := newFixture(t)
			storetest.RunSequence(t, storetest.Fixture{
				Store: store,
				Reopen: func() datastore.Store {
					client, err := postgres.New(openTestPool(t), namespace)
					if err != nil {
						t.Fatal(err)
					}
					return client
				},
			}, commands)
		})
	}
}
