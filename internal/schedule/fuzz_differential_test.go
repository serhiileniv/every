package schedule

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A systematically generated corpus, run through BOTH implementations, with
// every accept/reject decision AND every rejection message compared.
//
// The frozen table in grammar.json pins the cases a human thought of. This
// pins the ones nobody did: it enumerates the grammar's whole input space --
// every unit, every boundary hour and minute, every day word, every
// punctuation mistake, every case variant -- and asks whether the two parsers
// agree on all of it. If they do, "the syntax did not change" stops being a
// claim about the cases I remembered to check.
func TestGrammarAgreesWithRubyOverGeneratedCorpus(t *testing.T) {
	ruby, root := rubyOrSkip(t)
	corpus := generateCorpus()
	t.Logf("corpus: %d schedule inputs", len(corpus))

	want := rubyParseAll(t, ruby, root, corpus)

	var (
		disagreeDecision int
		disagreeMessage  int
		disagreeRecord   int
		shown            int
	)
	for i, tokens := range corpus {
		got, err := Parse(tokens)
		rubyRes := want[i]

		if (err == nil) != rubyRes.OK {
			disagreeDecision++
			if shown < 15 {
				shown++
				t.Errorf("%q: go ok=%v, ruby ok=%v (go err: %v, ruby err: %s)",
					tokens, err == nil, rubyRes.OK, err, rubyRes.Error)
			}
			continue
		}

		if err != nil {
			if err.Error() != rubyRes.Error {
				disagreeMessage++
				if shown < 15 {
					shown++
					t.Errorf("%q message:\n  go   %q\n  ruby %q", tokens, err.Error(), rubyRes.Error)
				}
			}
			continue
		}

		gotJSON, mErr := MarshalRecord(got.ToRecord())
		if mErr != nil {
			t.Fatal(mErr)
		}
		if string(gotJSON) != rubyRes.ToH {
			disagreeRecord++
			if shown < 15 {
				shown++
				t.Errorf("%q record:\n  go   %s\n  ruby %s", tokens, gotJSON, rubyRes.ToH)
			}
		}
	}

	if n := disagreeDecision + disagreeMessage + disagreeRecord; n > 0 {
		t.Errorf("TOTAL disagreements: %d decisions, %d messages, %d records",
			disagreeDecision, disagreeMessage, disagreeRecord)
	}
}

type rubyParse struct {
	OK    bool   `json:"ok"`
	ToH   string `json:"to_h"`
	Error string `json:"error"`
}

func rubyOrSkip(t *testing.T) (ruby, root string) {
	t.Helper()
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("no ruby on PATH; the Ruby tree is what this port replaces")
	}
	root, err = filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "lib", "every.rb")); err != nil {
		t.Skip("Ruby tree removed; differential comparison no longer applies")
	}
	return ruby, root
}

// rubyParseAll sends the whole corpus to one interpreter and reads the answers
// back. One process, not one per case -- the corpus is far too large for that.
func rubyParseAll(t *testing.T, ruby, root string, corpus [][]string) []rubyParse {
	t.Helper()

	payload, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(in, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	const script = `
$LOAD_PATH.unshift File.join(ARGV[0], "lib")
require "every"
require "json"
out = JSON.parse(File.read(ARGV[1])).map do |tokens|
  begin
    s = Every::Schedule.parse(tokens)
    { "ok" => true, "to_h" => JSON.generate(s.to_h) }
  rescue ArgumentError => e
    { "ok" => false, "error" => e.message }
  end
end
puts JSON.generate(out)
`
	cmd := exec.Command(ruby, "-e", script, root, in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ruby: %v", err)
	}

	var res []rubyParse
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("decoding ruby output: %v", err)
	}
	if len(res) != len(corpus) {
		t.Fatalf("ruby returned %d results for %d inputs", len(res), len(corpus))
	}
	return res
}

// generateCorpus enumerates the grammar's input space plus the ways users get
// it wrong.
func generateCorpus() [][]string {
	var corpus [][]string
	add := func(tokens ...string) { corpus = append(corpus, tokens) }

	// --- single-token forms -------------------------------------------------
	// Every unit, across the interesting magnitudes and both boundaries of the
	// 10-second floor.
	for _, n := range []string{
		"0", "1", "5", "9", "10", "11", "59", "60", "61", "90",
		"100", "119", "120", "3599", "3600", "3601", "86400",
		"007", "0010",
	} {
		for _, unit := range []string{"s", "m", "h", "S", "M", "H", "d", "w", "y", ""} {
			add(n + unit)
		}
	}
	// Numeric and unit malformations.
	for _, tok := range []string{
		"hourly", "HOURLY", "Hourly", "hourly ", " hourly", "hourlyy", "hour",
		"daily", "minutely", "-5m", "+5m", "5.5m", "5,m", "5m5", "m5", "5 m",
		"", " ", "banana", "15", "s", "1d", "25h99", "∞m", "5m\n", "\t5m",
		"99999999999999999999s", "18446744073709551616s",
	} {
		add(tok)
	}

	// --- two-token calendar forms ------------------------------------------
	days := []string{
		"day", "daily", "weekdays", "weekends",
		"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday",
		"Monday", "MONDAY", "mOnDaY",
		"monday,thursday", "monday,monday", "sunday,saturday",
		"monday,tuesday,wednesday,thursday,friday",
		"monday,", ",monday", "monday,,thursday", ",", "",
		"mon", "tues", "weekday", "weekend", "days", "everyday", "banana",
		"day,weekdays", "weekdays,weekends",
	}
	times := []string{
		"9am", "9AM", "9Am", "9pm", "12am", "12pm", "1am", "1pm",
		"0am", "13pm", "0pm", "13am", "24am",
		"0", "9", "12", "13", "23", "24", "25", "99",
		"00:00", "09:05", "9:05", "17:30", "23:59", "24:00", "23:60", "9:5",
		"9:005", "9:", ":30", "9::30", "9.30", "9-30",
		"9am,6pm", "9am,9am", "9am,", ",9am", "9am,,6pm", ",", "",
		"9am,6pm,11pm", "0:00,23:59",
		"noon", "midnight", "banana", "9am pm", "1230",
	}
	for _, d := range days {
		for _, tm := range times {
			add(d, tm)
		}
	}

	// --- wrong arity --------------------------------------------------------
	add("day", "9am", "6pm")
	add("15m", "extra")
	add("day")
	add("weekdays")
	add("monday", "10:00", "extra", "more")
	add("", "")
	add("", "9am")
	add("day", "")

	return corpus
}

// A quick sanity check that the corpus actually covers both outcomes -- a
// corpus that only contains rejections would pass the differential trivially.
func TestCorpusCoversBothOutcomes(t *testing.T) {
	corpus := generateCorpus()
	accepted, rejected := 0, 0
	for _, tokens := range corpus {
		if _, err := Parse(tokens); err == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted < 100 {
		t.Errorf("only %d accepted inputs; the corpus is not exercising success", accepted)
	}
	if rejected < 100 {
		t.Errorf("only %d rejected inputs; the corpus is not exercising failure", rejected)
	}
	t.Logf("corpus outcomes: %d accepted, %d rejected, %d total",
		accepted, rejected, len(corpus))
	if strings.Contains(fmt.Sprint(len(corpus)), "x") {
		t.Skip()
	}
}
