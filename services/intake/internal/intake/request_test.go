package intake

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func validSubmission() Submission {
	now := time.Now().UTC()
	return Submission{ReceiptMode: ReceiptAsync, Source: Source{Connection: Connection{Provider: "fixture", Account: "workspace:one"}, Scope: "channel/one", Kind: "message", ID: "123.456"}, Author: "user:one", SubmittedBy: "user:one", Body: "please investigate", Instruction: "investigate", CreatedAt: now, ObservedAt: now, Metadata: jsontext.Value(`{"channel":"channel/one"}`)}
}

func TestRequestIdentityIncludesEverySourceComponent(t *testing.T) {
	base := validSubmission().Source
	original, err := base.RequestID()
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Source){
		func(s *Source) { s.Connection.Provider = "other" },
		func(s *Source) { s.Connection.Account += ":other" },
		func(s *Source) { s.Scope += ":other" },
		func(s *Source) { s.Kind += ":other" },
		func(s *Source) { s.ID += ":other" },
	} {
		changed := base
		change(&changed)
		id, err := changed.RequestID()
		if err != nil || id == original {
			t.Fatalf("source identity collapsed: %+v %v", changed, err)
		}
	}
	left, right := base, base
	left.Scope, left.ID = "a:b", "c"
	right.Scope, right.ID = "a", "b:c"
	a, _ := left.RequestID()
	b, _ := right.RequestID()
	if a == b {
		t.Fatal("opaque delimiters caused identity collision")
	}
}

func TestSubmissionBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Submission)
	}{
		{"missing submitter", func(s *Submission) { s.SubmittedBy = "" }},
		{"invalid provider", func(s *Submission) { s.Source.Connection.Provider = "../github" }},
		{"missing account", func(s *Submission) { s.Source.Connection.Account = "" }},
		{"oversize source", func(s *Submission) { s.Source.ID = strings.Repeat("x", 513) }},
		{"empty instruction", func(s *Submission) { s.Instruction = " \n" }},
		{"oversize instruction", func(s *Submission) { s.Instruction = strings.Repeat("x", (16<<10)+1) }},
		{"oversize body", func(s *Submission) { s.Body = strings.Repeat("x", (64<<10)+1) }},
		{"invalid text", func(s *Submission) { s.Body = "\xff" }},
		{"nul", func(s *Submission) { s.Instruction = "x\x00y" }},
		{"missing time", func(s *Submission) { s.ObservedAt = time.Time{} }},
		{"invalid metadata", func(s *Submission) { s.Metadata = jsontext.Value(`{"broken"`) }},
		{"oversize metadata", func(s *Submission) { s.Metadata = jsontext.Value(`"` + strings.Repeat("x", 16<<10) + `"`) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := validSubmission()
			test.mutate(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("invalid submission accepted")
			}
		})
	}
	if err := validSubmission().Validate(); err != nil {
		t.Fatal(err)
	}
}

func FuzzSubmission(f *testing.F) {
	seed, _ := json.Marshal(validSubmission())
	f.Add(seed)
	f.Add([]byte(`null`))
	f.Add([]byte(`{"Source":{"Connection":{"Provider":"slack","Account":"T1"}}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 128<<10 {
			return
		}
		var submission Submission
		if err := json.Unmarshal(raw, &submission); err != nil || submission.Validate() != nil {
			return
		}
		id, err := submission.Source.RequestID()
		if err != nil || len(id) != len(submission.Source.Connection.Provider)+65 || !strings.HasPrefix(id, submission.Source.Connection.Provider+":") {
			t.Fatalf("invalid request identity %q %v", id, err)
		}
		encoded, err := json.Marshal(Request{ID: id, Submission: submission})
		if err != nil {
			t.Fatal(err)
		}
		var restored Request
		if err := json.Unmarshal(encoded, &restored); err != nil {
			t.Fatal(err)
		}
		restoredID, err := restored.Source.RequestID()
		if err != nil || restoredID != id || restored.Instruction != submission.Instruction || restored.Source != submission.Source {
			t.Fatal("request did not survive persistence encoding")
		}
	})
}

func FuzzSourceIdentity(f *testing.F) {
	f.Add("account:1", "scope/one", "message", "123.456")
	f.Add("account", "a:b", "message", "c")
	f.Fuzz(func(t *testing.T, account, scope, kind, sourceID string) {
		source := Source{Connection: Connection{Provider: "fixture", Account: account}, Scope: scope, Kind: kind, ID: sourceID}
		id, err := source.RequestID()
		if err != nil {
			return
		}
		changed := source
		changed.Connection.Provider = "other"
		other, err := changed.RequestID()
		if err != nil || other == id {
			t.Fatal("provider identity collision")
		}
		again, err := source.RequestID()
		if err != nil || again != id {
			t.Fatal("unstable request identity")
		}
	})
}

func TestSubmissionRequiresExplicitReceiptPolicy(t *testing.T) {
	submission := validSubmission()
	submission.ReceiptMode = ""
	if err := submission.Validate(); err == nil {
		t.Fatal("missing receipt policy accepted")
	}
	submission.ReceiptMode = "unknown"
	if err := submission.Validate(); err == nil {
		t.Fatal("unknown receipt policy accepted")
	}
}
