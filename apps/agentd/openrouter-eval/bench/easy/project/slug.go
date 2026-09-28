package easy

// Slug turns a title into a URL slug: ASCII letters are lower-cased, ASCII
// letters and digits are kept, and every run of any other characters
// becomes a single "-". The result never starts or ends with "-".
//
//	Slug("Hello, World!")   == "hello-world"
//	Slug("  Go 1.22 notes") == "go-1-22-notes"
//	Slug("***")             == ""
func Slug(title string) string {
	return ""
}
