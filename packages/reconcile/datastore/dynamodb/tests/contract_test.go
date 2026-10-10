package dynamodbtests

import (
	"fmt"
	"testing"

	"github.com/p5/sir-robs-a-bot/packages/reconcile/datastore/storetest"
)

func TestStoreContract(t *testing.T) {
	storetest.Run(t, contractFixture)
}

func TestStoreOperationSequences(t *testing.T) {
	for index, sequence := range storetest.SequenceCorpus() {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			storetest.RunSequence(t, contractFixture(t), sequence)
		})
	}
}

func TestInspectionContract(t *testing.T) {
	storetest.RunInspection(t, contractFixture)
}
