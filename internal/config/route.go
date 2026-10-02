package config

// Route is the [route] table: which lines typed at the prompt go to the
// assistant besides those with a ? or @ prefix and skills. A line whose
// first word is a command, an alias or a function is bash's whatever it
// looks like. The shell reads the table once, when aish starts it.
type Route struct {
	// Capital takes a line that starts with a capital letter.
	Capital bool `toml:"capital"`
	// NotFound takes a line bash would only answer with "command not
	// found": MinWords words or more and no | & ; < > ( ) $ ` \ =. With it
	// off, the first such line hints at the ? prefix.
	NotFound bool `toml:"not_found"`
	// Suffix takes a line that ends with it; "" takes none.
	Suffix   string `toml:"suffix"`
	MinWords int    `toml:"min_words"`
}
