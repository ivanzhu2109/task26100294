package fence

// tokenize 把正文或查询切分为词：连续英文字母为词，忽略大小写，
// 其余字符均为分隔符。通过 intern 回调把词映射为词ID。
func tokenize(s string, intern func(string) uint32) []uint32 {
	var out []uint32
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out = append(out, intern(lowerASCII(s[start:end])))
			start = -1
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(s))
	return out
}

// lowerASCII 把纯 ASCII 字母串转小写，避免 strings.ToLower 的分配。
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}
