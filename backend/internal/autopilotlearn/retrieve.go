package autopilotlearn

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Similar is a past decision that resembles a new question.
type Similar struct {
	Pair  Pair
	Score float64
}

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`the and for with that this from are was were you your not but can will should would could
		have has had into then than them they what when where which who how why its our out all any one two use used using
		een het van voor met dat dit dan ook niet maar kan zal wel nog naar bij als zijn wordt worden die dus mag
		ja nee yes please option optie`) {
		m[w] = true
	}
	return m
}()

func tokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) < 3 || stopwords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

func pairText(p Pair) string {
	return p.Question + " " + strings.Join(p.Options, " ")
}

// SimilarPairs returns up to k past decisions most like the question (and its
// options), best first. It is a small TF-IDF cosine over the stored pairs: the
// store holds at most a few thousand short texts, so nothing heavier is needed.
// Pairs from the same project and corrections rank a little higher. Pairs that
// share no word with the question are never returned.
func SimilarPairs(pairs []Pair, project, question string, options []string, k int) []Similar {
	if k <= 0 || len(pairs) == 0 {
		return nil
	}
	qTokens := tokens(question + " " + strings.Join(options, " "))
	if len(qTokens) == 0 {
		return nil
	}
	docs := make([]map[string]float64, len(pairs))
	df := map[string]int{}
	for i, p := range pairs {
		tf := map[string]float64{}
		for _, t := range tokens(pairText(p)) {
			tf[t]++
		}
		for t := range tf {
			df[t]++
		}
		docs[i] = tf
	}
	n := float64(len(pairs))
	idf := func(t string) float64 { return math.Log(1+n/float64(1+df[t])) + 0.1 }
	vec := func(tf map[string]float64) (map[string]float64, float64) {
		v := make(map[string]float64, len(tf))
		var norm float64
		for t, c := range tf {
			w := (1 + math.Log(c)) * idf(t)
			v[t] = w
			norm += w * w
		}
		return v, math.Sqrt(norm)
	}
	qtf := map[string]float64{}
	for _, t := range qTokens {
		qtf[t]++
	}
	qv, qn := vec(qtf)
	if qn == 0 {
		return nil
	}
	var out []Similar
	for i, p := range pairs {
		dv, dn := vec(docs[i])
		if dn == 0 {
			continue
		}
		var dot float64
		for t, w := range qv {
			dot += w * dv[t]
		}
		if dot == 0 {
			continue
		}
		score := dot / (qn * dn)
		if project != "" && p.Project == project {
			score += 0.12
		}
		if p.Source == SourceCorrection {
			score += 0.08
		}
		out = append(out, Similar{Pair: p, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Pair.At.After(out[j].Pair.At)
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

var (
	optionLine = regexp.MustCompile(`^\s*(?:[-*•]|\d{1,2}[.)]|[A-Da-d][.)]|(?i:option|optie)\s+[A-Za-z0-9][:.)])\s+(\S.*)$`)
	recMark    = regexp.MustCompile(`(?i)\(\s*(?:recommended|aanbevolen)\s*\)|\b(?:recommended|aanbevolen)\b`)
	recSentRe  = regexp.MustCompile(`(?i)(?:my recommendation|i recommend|i'd recommend|ik raad|mijn advies|aanbeveling|recommend)[^.\n]*`)
	mdMarkers  = strings.NewReplacer("**", "", "__", "", "`", "")
)

// ExtractOptions reads the options and the recommendation out of the orchestrator
// message a question came from. It is deliberately simple and strict: a list item
// is an option, an item or sentence marked "recommended" is the recommendation.
func ExtractOptions(message string) (options []string, recommendation string) {
	for _, line := range strings.Split(message, "\n") {
		m := optionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(mdMarkers.Replace(m[1]))
		if text == "" || len(options) >= MaxOptions {
			continue
		}
		options = append(options, clip(text, MaxOptionRunes))
		if recommendation == "" && recMark.MatchString(line) {
			recommendation = clip(strings.TrimSpace(recMark.ReplaceAllString(text, "")), MaxOptionRunes)
		}
	}
	if recommendation == "" {
		if s := recSentRe.FindString(mdMarkers.Replace(message)); s != "" {
			recommendation = clip(s, MaxOptionRunes)
		}
	}
	return options, recommendation
}
