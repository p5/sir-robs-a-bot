package resourceunit

import (
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore/memory"
	"github.com/p5/sir-robs-a-bot/packages/resources/datastore/storetest"
	"testing"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storetest.Fixture {
		store := memory.New()
		return storetest.Fixture{Store: store, Reopen: func() datastore.Store { return store }}
	})
}
