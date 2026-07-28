package store

import "testing"

func TestOpenMemory_CreatesCurrentSchema(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	// Core tables must exist.
	if err := db.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("events table missing: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected empty, got %d", n)
	}
	if err := db.QueryRow(`SELECT count(*) FROM session_runs`).Scan(&n); err != nil {
		t.Fatalf("session_runs table missing: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM run_attempts`).Scan(&n); err != nil {
		t.Fatalf("run_attempts table missing: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM tool_steps`).Scan(&n); err != nil {
		t.Fatalf("tool_steps table missing: %v", err)
	}
}

func TestOpenMemory_Isolated(t *testing.T) {
	a, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.Exec(`INSERT INTO environments (id,name,config_type,body,created_at,updated_at) VALUES ('env_x','n','cloud','{}','t','t')`); err != nil {
		t.Fatal(err)
	}
	b, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var n int
	if err := b.QueryRow(`SELECT count(*) FROM environments`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("second OpenMemory must be isolated, saw %d rows", n)
	}
}
