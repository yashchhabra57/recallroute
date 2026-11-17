package pgstore

import (
	"context"
	"os"
	"testing"

	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/store/storetest"
)

// Set RECALLROUTE_TEST_DATABASE_URL to run against a real PostgreSQL,
// e.g. postgres://postgres:postgres@localhost:5432/recallroute_test?sslmode=disable
// CI runs this job with a postgres service container.
func TestContract(t *testing.T) {
	url := os.Getenv("RECALLROUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RECALLROUTE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := Open(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Reset(ctx); err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestMigrationsAreIdempotent(t *testing.T) {
	url := os.Getenv("RECALLROUTE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RECALLROUTE_TEST_DATABASE_URL not set")
	}
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), url)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		s.Close()
	}
}
