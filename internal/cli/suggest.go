package cli

// Suggesting a command on a typo, the way git and cargo do.
//
// The whole feature hangs on one guard: `every 15m` is not a command either,
// and must never be met with "did you mean list". every's first argument is
// overloaded -- a verb OR a schedule -- so a suggester that fires on anything
// unrecognised would insult every correct interval invocation that happens to
// be a few edits from a verb.

// suggestable maps every spelling a typo can be corrected to onto the command
// it means.
//
// Aliases are matched but never suggested: someone who typed `lst` is one edit
// from both `ls` and `list`, and answering with the alias would be technically
// a fix and practically a worse one. Collapsing them also turns that tie into
// no tie at all -- see suggestCommand.
var suggestable = map[string]string{
	"list": "list", "ls": "list",
	"rm": "rm", "remove": "rm",
	"inspect": "inspect", "show": "inspect",
	"log": "log", "run": "run", "pause": "pause", "resume": "resume",
	"doctor": "doctor", "exists": "exists", "set": "set",
	"schema": "schema", "version": "version", "help": "help",
}

// suggestCommand returns the closest command to tok, or "" when nothing is
// close enough to be worth saying.
//
// The threshold scales with length because edit distance means less on short
// words: at a flat 2, "rm" would suggest "run" and "ls" would suggest "log",
// turning a correct-looking typo into a confident wrong answer. Under five
// characters the bar is one edit.
func suggestCommand(tok string) string {
	if tok == "" {
		return ""
	}
	limit := 2
	if len(tok) < 5 {
		limit = 1
	}

	// Scored over every spelling, collected by the command each means. Map
	// iteration order is random in Go, so the winner must not depend on it.
	bestDist := limit + 1
	winners := map[string]bool{}
	for spelling, cmd := range suggestable {
		switch d := editDistance(tok, spelling); {
		case d < bestDist:
			bestDist = d
			winners = map[string]bool{cmd: true}
		case d == bestDist:
			winners[cmd] = true
		}
	}
	if bestDist > limit {
		return ""
	}
	// One command, however many of its spellings tied: `lst` is one edit from
	// both `ls` and `list`, which are the same command, so there is an answer.
	// Two different commands is a real tie -- `rn` is one edit from both `rm`
	// and `run` -- and picking by iteration order would be confidently wrong
	// half the time. Saying nothing leaves the user the command list.
	if len(winners) != 1 {
		return ""
	}
	for cmd := range winners {
		return cmd
	}
	return ""
}

// editDistance is Levenshtein, two rows rather than a full matrix.
//
// Bounded by the command list: the longest is "version", so the allocation is
// never interesting and the clarity is worth more than reusing a buffer.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
