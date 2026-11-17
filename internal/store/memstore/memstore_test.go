package memstore

import (
	"testing"

	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) store.Store { return New() })
}
