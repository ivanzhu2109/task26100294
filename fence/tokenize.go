package fence

// tokenize splits text into lowercase words: maximal runs of ASCII
// letters; every other byte is a separator.
func tokenize(text string) []string {
	var words []string
	start := -1
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'a' && c <= 'z' {
			if start < 0 {
				start = i
			}
			continue
		}
		if c >= 'A' && c <= 'Z' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words = append(words, lower(text[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, lower(text[start:]))
	}
	return words
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
