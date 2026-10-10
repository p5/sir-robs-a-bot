package github

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"
)

func TestMentionSyntax(t *testing.T) {
	for _, test := range []struct {
		body        string
		instruction string
	}{
		{"@sir-robs-a-bot fix this", "fix this"},
		{" \n@Sir-Robs-A-Bot\nexplain this\nplease", "explain this\nplease"},
		{"hello @sir-robs-a-bot fix this", ""},
		{"> @sir-robs-a-bot fix this", ""},
		{"```\n@sir-robs-a-bot fix this\n```", ""},
		{"@sir-robs-a-bot-extra fix this", ""},
		{"@sir-robs-a-bot", ""},
		{"@sir-robs-a-bot \n", ""},
		{"@sir-robs-a-bot: fix this", ""},
		{"@sir-robs-a-bot fix\x00this", ""},
		{"@sir-robs-a-bot " + strings.Repeat("x", (16<<10)+1), ""},
	} {
		t.Run(test.body[:min(len(test.body), 50)], func(t *testing.T) {
			instruction, ok := ParseMention(test.body, "sir-robs-a-bot")
			if instruction != test.instruction || ok != (test.instruction != "") {
				t.Fatalf("got %q,%v", instruction, ok)
			}
		})
	}
}

func TestPolicyRejectsUnauthorizedAndHistoricalSources(t *testing.T) {
	floor := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	policy := Policy{Account: User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true, 42: true}, Since: floor}
	source := Source{ID: 1, Number: 2, Body: "@sir-robs-a-bot fix this", User: User{ID: 7, Login: "p5", Type: "User"}, CreatedAt: floor}
	repository := Repository{ID: 11, FullName: "p5/test"}
	request, ok, err := policy.Candidate(repository, "comment", source)
	if err != nil || !ok || request.Source.Connection != Connection(42) || request.Source.ID != "1" {
		t.Fatalf("accept: %+v %v %v", request, ok, err)
	}
	cases := []Source{source, source, source, source}
	cases[0].User.ID = 99
	cases[1].User.Type = "Bot"
	cases[2].User.ID = 42
	cases[3].CreatedAt = floor.Add(-time.Second)
	for _, candidate := range cases {
		if _, ok, err := policy.Candidate(repository, "comment", candidate); err != nil || ok {
			t.Fatalf("unauthorized source accepted: %+v %v", candidate, err)
		}
	}
	policy.AllowedUsers = nil
	if _, _, err := policy.Candidate(repository, "comment", source); err == nil {
		t.Fatal("empty allowlist accepted")
	}
}

func FuzzMention(f *testing.F) {
	for _, seed := range []string{"@sir-robs-a-bot fix this", "> @sir-robs-a-bot fix this", "\xff", "@sir-robs-a-bot\x00fix"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		instruction, ok := ParseMention(body, "sir-robs-a-bot")
		if ok && (instruction == "" || len(instruction) > 16<<10 || strings.ContainsRune(instruction, 0)) {
			t.Fatal("invalid instruction accepted")
		}
	})
}

func FuzzSourceAuthorization(f *testing.F) {
	for _, seed := range []string{`{"id":1,"number":2,"body":"@sir-robs-a-bot fix this","user":{"id":7,"type":"User"},"created_at":"2026-10-10T01:00:00Z"}`, `{"user":{"id":99,"type":"User"}}`, `null`, `{"id":9223372036854775808}`} {
		f.Add([]byte(seed))
	}
	floor := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	policy := Policy{Account: User{ID: 42, Login: "sir-robs-a-bot"}, AllowedUsers: map[int64]bool{7: true}, Since: floor}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 64<<10 {
			return
		}
		var source Source
		if err := json.Unmarshal(raw, &source); err != nil {
			return
		}
		accepted, ok, err := policy.Candidate(Repository{ID: 11, FullName: "p5/test"}, "comment", source)
		if err == nil && ok && (accepted.Author != "7" || source.User.Type != "User" || accepted.CreatedAt.Before(floor) || accepted.Instruction == "") {
			t.Fatal("source bypassed authorization")
		}
	})
}
