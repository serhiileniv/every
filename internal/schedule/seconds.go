package schedule

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Seconds is an interval in seconds, held at arbitrary precision.
//
// Ruby's integers have no width, so `every 99999999999999999999s` parses there
// and writes that value straight into a plist. Nobody types it -- but two
// things follow from Ruby having accepted it, and both matter more than the
// input itself:
//
//   - A tasks.json somewhere may contain such a value. Decoding it into an
//     int64 fails, which would turn a task into an "invalid" row, or worse
//     refuse to load the store at all. Reading old state has to keep working.
//   - The syntax is frozen. "Go rejects an input Ruby accepted" is a change to
//     the grammar however absurd the input, and the promise was that there
//     would be none.
//
// So the exact digits are preserved for rendering and for JSON, and a
// saturating int64 is offered for the arithmetic that has to happen anyway.
// Saturation is safe here: a value past MaxInt64 is 292 billion years, so
// every comparison and every "next run" estimate lands in the same place a
// wider type would put it.
type Seconds struct {
	text string // exact decimal, as written; "" means zero
	n    int64  // saturated at MaxInt64 when text does not fit
}

// SecondsOf builds an interval from a value that already fits.
func SecondsOf(n int64) Seconds {
	return Seconds{text: strconv.FormatInt(n, 10), n: n}
}

// parseSeconds reads a non-negative decimal digit string of any length.
func parseSeconds(digits string) (Seconds, error) {
	v, ok := new(big.Int).SetString(digits, 10)
	if !ok || v.Sign() < 0 {
		return Seconds{}, fmt.Errorf("not a decimal integer: %q", digits)
	}
	return secondsFromBig(v), nil
}

func secondsFromBig(v *big.Int) Seconds {
	// Render without leading zeros, which is what Ruby's to_s does and
	// therefore what a plist and a JSON number must contain.
	text := v.String()
	if v.IsInt64() {
		return Seconds{text: text, n: v.Int64()}
	}
	return Seconds{text: text, n: math.MaxInt64}
}

// Mul multiplies by a small unit factor at full precision.
func (s Seconds) Mul(factor int64) Seconds {
	if s.text == "" {
		return Seconds{}
	}
	v, _ := new(big.Int).SetString(s.text, 10)
	return secondsFromBig(v.Mul(v, big.NewInt(factor)))
}

// String is the exact decimal. Everything that renders an interval into a
// plist, a unit file or JSON goes through here, so no precision is lost on the
// way to disk.
func (s Seconds) String() string {
	if s.text == "" {
		return "0"
	}
	return s.text
}

// Int64 is the saturating value, for arithmetic and comparison.
func (s Seconds) Int64() int64 { return s.n }

// IsZero reports an absent interval, which is how a calendar schedule is
// distinguished from one that genuinely repeats every zero seconds (there is
// no such thing -- the floor is ten).
func (s Seconds) IsZero() bool { return s.text == "" || s.text == "0" }

// Cmp compares against a small value without widening the caller.
func (s Seconds) Cmp(n int64) int {
	switch {
	case s.n < n:
		return -1
	case s.n > n:
		return 1
	default:
		return 0
	}
}

// Mod is the remainder against a small divisor, used to pick the unit a human
// would have typed. Exact even for values past int64.
func (s Seconds) Mod(d int64) int64 {
	if s.text == "" {
		return 0
	}
	v, _ := new(big.Int).SetString(s.text, 10)
	return new(big.Int).Mod(v, big.NewInt(d)).Int64()
}

// Div is exact integer division, for the same purpose.
func (s Seconds) Div(d int64) string {
	if s.text == "" {
		return "0"
	}
	v, _ := new(big.Int).SetString(s.text, 10)
	return new(big.Int).Div(v, big.NewInt(d)).String()
}

// MarshalJSON emits a bare JSON number, at full precision.
func (s Seconds) MarshalJSON() ([]byte, error) {
	if s.text == "" {
		return []byte("0"), nil
	}
	return []byte(s.text), nil
}

// UnmarshalJSON accepts any JSON number, including one wider than int64 --
// which is exactly the state an older every may have written.
func (s *Seconds) UnmarshalJSON(b []byte) error {
	text := strings.TrimSpace(string(b))
	if text == "null" || text == "" {
		*s = Seconds{}
		return nil
	}
	// A float form ("900.0") is not something either implementation writes,
	// but tolerating it costs nothing and refusing it would strand a
	// hand-edited store.
	if i := strings.IndexAny(text, ".eE"); i >= 0 {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return err
		}
		*s = SecondsOf(int64(f))
		return nil
	}
	parsed, err := parseSeconds(text)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}
