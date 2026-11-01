package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownToTelegramHTML(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{
			name: "fenced JSON code block with language",
			text: "Before\n```json\n{\"count\":1}\n```\nAfter",
			want: "Before\n<pre><code>{\"count\":1}\n</code></pre>\nAfter",
		},
		{
			name: "HTML escaping in normal and code text",
			text: "2 < 3 & 4 > 1\n```html\n<b>&</b>\n```",
			want: "2 &lt; 3 &amp; 4 &gt; 1\n<pre><code>&lt;b&gt;&amp;&lt;/b&gt;\n</code></pre>",
		},
		{
			name: "inline code",
			text: "Run `echo <ok>` now",
			want: "Run <code>echo &lt;ok&gt;</code> now",
		},
		{
			name: "ordinary text",
			text: "Hello, world.\nSecond line.",
			want: "Hello, world.\nSecond line.",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := markdownToTelegramHTML(test.text); got != test.want {
				t.Fatalf("markdownToTelegramHTML(%q) = %q, want %q", test.text, got, test.want)
			}
		})
	}
}

func TestSplitTelegramMarkdownKeepsCodeBlocksBalanced(t *testing.T) {
	text := "Before\n```sh\n" + strings.Repeat("printf ok\n", 300) + "```\nAfter"
	chunks := splitTelegramMarkdown(text, 2000)
	if len(chunks) < 2 {
		t.Fatalf("splitTelegramMarkdown returned %d chunks, want multiple", len(chunks))
	}
	for index, chunk := range chunks {
		converted := markdownToTelegramHTML(chunk)
		if strings.Count(converted, "<pre><code>") != strings.Count(converted, "</code></pre>") {
			t.Errorf("chunk %d has unbalanced code block tags: %q", index, converted)
		}
	}
}

func TestSplitTelegramMarkdownPreservesPlainText(t *testing.T) {
	text := strings.Repeat("plain🙂 ", 400)
	chunks := splitTelegramMarkdown(text, 2000)
	if len(chunks) < 2 {
		t.Fatalf("splitTelegramMarkdown returned %d chunks, want multiple", len(chunks))
	}
	if got := strings.Join(chunks, ""); got != text {
		t.Fatalf("joined chunks = %q, want original text", got)
	}
	for index, chunk := range chunks {
		if size := len([]rune(chunk)); size > 2000 {
			t.Errorf("chunk %d contains %d runes, want at most 2000", index, size)
		}
	}
}
