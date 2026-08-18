package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orkcom-tech/contextverse/internal/storage"
)

// newSpace is a space this build just made — the right starting point here,
// because delete is about what happens to a file THIS build wrote and
// versioned. testspace.Legacy is for the upgrade cases.
func newSpace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := run(t, "--dir", dir, "init", "solo", "--name", "test", "--role", "test"); err != nil {
		t.Fatalf("init solo: %v\n%s", err, out)
	}
	return dir
}

// A file could be soft-deleted by the storage layer and by nothing a person
// could type. `file undelete` restores a soft-deleted file and `file destroy`
// refuses a live version with "soft-delete first", so the CLI told you to do a
// thing it gave you no way to do.

func write(t *testing.T, dir, path, body string) {
	t.Helper()
	out, err := runIn(t, strings.NewReader(body), "--dir", dir, "file", "put", path, "--from", "-")
	if err != nil {
		t.Fatalf("put %s: %v\n%s", path, err, out)
	}
}

func TestDeleteRemovesTheFileAndKeepsItsHistory(t *testing.T) {
	dir := newSpace(t)
	write(t, dir, "team/policy.md", "one\n")
	write(t, dir, "team/policy.md", "two\n")

	out, err := run(t, "--dir", dir, "file", "delete", "team/policy.md")
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	if !strings.Contains(out, "undelete") {
		t.Errorf("the message must say how to get it back: %q", out)
	}

	// Gone from the space.
	listing, err := run(t, "--dir", dir, "file", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listing, "team/policy.md") {
		t.Errorf("a deleted file is still listed:\n%s", listing)
	}

	// And gone from the working tree, or the next list shows a file the space
	// does not have.
	if _, err := os.Stat(filepath.Join(dir, "team", "policy.md")); !os.IsNotExist(err) {
		t.Errorf("the working tree copy survived the delete: %v", err)
	}

	// But the history is there, which is the whole difference between this and
	// destroy.
	hist, err := run(t, "--dir", dir, "file", "history", "team/policy.md")
	if err != nil {
		t.Fatalf("history after delete: %v\n%s", err, hist)
	}
	for _, want := range []string{"1", "2"} {
		if !strings.Contains(hist, want) {
			t.Errorf("version %s is missing from the history after a delete:\n%s", want, hist)
		}
	}

	// And it comes back.
	if out, err := run(t, "--dir", dir, "file", "undelete", "team/policy.md"); err != nil {
		t.Fatalf("undelete: %v\n%s", err, out)
	}
	body, err := run(t, "--dir", dir, "file", "get", "team/policy.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(body) != "two" {
		t.Errorf("undelete restored %q, want the last live version", body)
	}
}

func TestDeleteSaysWhatItDidInJSON(t *testing.T) {
	dir := newSpace(t)
	write(t, dir, "team/policy.md", "one\n")

	raw, err := run(t, "--dir", dir, "file", "delete", "team/policy.md", "--json")
	if err != nil {
		t.Fatalf("delete --json: %v\n%s", err, raw)
	}
	var got FileDeleted
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, raw)
	}
	if got.Path != "team/policy.md" || !got.Deleted {
		t.Fatalf("%+v", got)
	}
	if got.Version == "" {
		t.Error("the version that was removed must be named — it is what undelete brings back")
	}
}

func TestDeleteRefusesWhenTheFileMovedUnderYou(t *testing.T) {
	dir := newSpace(t)
	write(t, dir, "team/policy.md", "one\n")

	// You read v1 …
	// … somebody else writes v2 …
	write(t, dir, "team/policy.md", "two\n")

	// … and your delete, aimed at v1, must not take their v2 with it.
	out, err := run(t, "--dir", dir, "file", "delete", "team/policy.md", "--if-version", "v1")
	if err == nil {
		t.Fatalf("a stale delete was accepted:\n%s", out)
	}
	// It has to be refused as a CONFLICT. The first cut of this test asserted
	// only err != nil and passed while --if-version was rejecting "v1" as
	// unparseable — a refusal for the wrong reason, which would have shipped a
	// flag nobody could use.
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("refused, but not as a conflict: %v", err)
	}
	body, gerr := run(t, "--dir", dir, "file", "get", "team/policy.md")
	if gerr != nil {
		t.Fatalf("the file is gone after a refused delete: %v", gerr)
	}
	if strings.TrimSpace(body) != "two" {
		t.Errorf("the refused delete changed the file: %q", body)
	}
}

func TestPutRefusesWhenTheFileMovedUnderYou(t *testing.T) {
	// The case that actually loses work: read an hour ago, written back now.
	// Without --if-version contextd compares against what is current at this
	// instant, which cannot see that at all.
	dir := newSpace(t)
	write(t, dir, "team/policy.md", "one\n")
	// You are looking at v1. Somebody else saves v2.
	write(t, dir, "team/policy.md", "two\n")

	out, err := runIn(t, strings.NewReader("one, edited\n"),
		"--dir", dir, "file", "put", "team/policy.md", "--from", "-", "--if-version", "v1")
	if err == nil {
		t.Fatalf("a stale write was accepted and the other version is gone:\n%s", out)
	}
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("refused, but not as a conflict: %v", err)
	}

	body, _ := run(t, "--dir", dir, "file", "get", "team/policy.md")
	if strings.TrimSpace(body) != "two" {
		t.Errorf("the refused write landed anyway: %q", body)
	}

	// And the ordinary case still works: the version you read is the one there.
	cur, err := runIn(t, strings.NewReader("two, edited\n"),
		"--dir", dir, "file", "put", "team/policy.md", "--from", "-", "--if-version", "v2")
	if err != nil {
		t.Fatalf("a write with the right expectation was refused: %v\n%s", err, cur)
	}
	body, _ = run(t, "--dir", dir, "file", "get", "team/policy.md")
	if strings.TrimSpace(body) != "two, edited" {
		t.Errorf("the accepted write did not land: %q", body)
	}
}

// --if-version takes what a person was just shown.
func TestIfVersionTakesTheFormEveryCommandPrints(t *testing.T) {
	dir := newSpace(t)
	write(t, dir, "team/policy.md", "one\n")

	// "v1" is what `file history` and `file put` print. The bare "1" is the
	// storage layer's own token and is accepted too, for a script that reads
	// --json.
	for _, form := range []string{"v1", "1"} {
		out, err := run(t, "--dir", dir, "file", "delete", "team/policy.md", "--if-version", form)
		if err != nil {
			t.Fatalf("--if-version %s was refused: %v\n%s", form, err, out)
		}
		if _, uerr := run(t, "--dir", dir, "file", "undelete", "team/policy.md"); uerr != nil {
			t.Fatal(uerr)
		}
	}

	// And nonsense is refused before it reaches storage, in words that say what
	// the flag wants.
	out, err := run(t, "--dir", dir, "file", "delete", "team/policy.md", "--if-version", "latest")
	if err == nil {
		t.Fatalf("--if-version latest was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "v4") {
		t.Errorf("the refusal must show the shape it wants: %v", err)
	}
}
