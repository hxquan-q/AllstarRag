package types

import (
	"fmt"
	"testing"
)

func TestIsGeneratedQuestionSource(t *testing.T) {
	const chunkID = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	cases := []struct {
		name       string
		chunkID    string
		sourceID   string
		sourceType SourceType
		want       bool
	}{
		{name: "body row", chunkID: chunkID, sourceID: chunkID, sourceType: ChunkSourceType},
		{
			name: "question row", chunkID: chunkID, sourceID: chunkID + "-q123",
			sourceType: ChunkSourceType, want: true,
		},
		{
			name: "hashed question row", chunkID: chunkID, sourceID: chunkID + "-q0123456789abcdef01234567",
			sourceType: ChunkSourceType, want: true,
		},
		{
			name: "image row with a suffix", chunkID: chunkID, sourceID: chunkID + "-image",
			sourceType: ImageSourceType,
		},
		{name: "empty chunk", sourceID: "-q1", sourceType: ChunkSourceType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsGeneratedQuestionSource(tc.chunkID, tc.sourceID, tc.sourceType); got != tc.want {
				t.Fatalf("IsGeneratedQuestionSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuestionGenerationOffIDs(t *testing.T) {
	off := &KnowledgeBase{ID: "off", Type: KnowledgeBaseTypeDocument}
	on := &KnowledgeBase{
		ID: "on", Type: KnowledgeBaseTypeDocument,
		QuestionGenerationConfig: &QuestionGenerationConfig{Enabled: true},
	}
	got := QuestionGenerationOffIDs([]*KnowledgeBase{off, on, nil})
	if _, ok := got["off"]; !ok {
		t.Fatal("closed document knowledge base was not marked")
	}
	if _, ok := got["on"]; ok {
		t.Fatal("enabled knowledge base was marked off")
	}
}

func TestQuestionGenerationDisabledLeavesFAQAlone(t *testing.T) {
	faq := &KnowledgeBase{Type: KnowledgeBaseTypeFAQ}
	if QuestionGenerationDisabled(faq) {
		t.Fatal("FAQ similar-question rows must not be treated as generated questions")
	}
	off := &KnowledgeBase{Type: KnowledgeBaseTypeDocument}
	if !QuestionGenerationDisabled(off) {
		t.Fatal("a document knowledge base with no question config is off")
	}
	on := &KnowledgeBase{
		Type:                     KnowledgeBaseTypeDocument,
		QuestionGenerationConfig: &QuestionGenerationConfig{Enabled: true},
	}
	if QuestionGenerationDisabled(on) || !QuestionGenerationActive(on) {
		t.Fatal("an enabled document knowledge base should keep generated questions")
	}
}

// 守卫 SQLite 谓词形态：必须保持 substr(...) = chunk_id || '-' 的写法，
// alias 为空与带表限定（"c"）两种形态都要正确拼出。
func TestGeneratedQuestionRowPredicateShape(t *testing.T) {
	want := fmt.Sprintf(
		"source_type = %d AND chunk_id <> '' AND source_id <> chunk_id AND "+
			"substr(source_id, 1, length(chunk_id) + 1) = chunk_id || '-'",
		int(ChunkSourceType),
	)
	if got := GeneratedQuestionRowPredicate(""); got != want {
		t.Fatalf(`GeneratedQuestionRowPredicate("") = %q, want %q`, got, want)
	}

	wantAliased := fmt.Sprintf(
		"c.source_type = %d AND c.chunk_id <> '' AND c.source_id <> c.chunk_id AND "+
			"substr(c.source_id, 1, length(c.chunk_id) + 1) = c.chunk_id || '-'",
		int(ChunkSourceType),
	)
	if got := GeneratedQuestionRowPredicate("c"); got != wantAliased {
		t.Fatalf(`GeneratedQuestionRowPredicate("c") = %q, want %q`, got, wantAliased)
	}
}

// 守卫 PG 谓词形态：必须保持 starts_with(source_id, chunk_id || '-')，
// 不能出现 ParadeDB pg_search 拒绝的列间比较（source_id <> chunk_id、substr 相等判断）。
func TestPostgresGeneratedQuestionRowPredicateShape(t *testing.T) {
	want := fmt.Sprintf(
		"source_type = %d AND chunk_id <> '' AND starts_with(source_id, chunk_id || '-')",
		int(ChunkSourceType),
	)
	if got := PostgresGeneratedQuestionRowPredicate(""); got != want {
		t.Fatalf(`PostgresGeneratedQuestionRowPredicate("") = %q, want %q`, got, want)
	}

	wantAliased := fmt.Sprintf(
		"c.source_type = %d AND c.chunk_id <> '' AND starts_with(c.source_id, c.chunk_id || '-')",
		int(ChunkSourceType),
	)
	if got := PostgresGeneratedQuestionRowPredicate("c"); got != wantAliased {
		t.Fatalf(`PostgresGeneratedQuestionRowPredicate("c") = %q, want %q`, got, wantAliased)
	}
}

// 守卫两个谓词输出不同：一旦 PG 调用方被误换回 SQLite 版（或反向），
// 这里会先红，提醒 pg_search 的 "Unsupported query shape" 约束。
func TestGeneratedQuestionPredicatesDiffer(t *testing.T) {
	for _, alias := range []string{"", "c"} {
		if GeneratedQuestionRowPredicate(alias) == PostgresGeneratedQuestionRowPredicate(alias) {
			t.Fatalf("alias %q: SQLite 与 PG 谓词输出相同，怀疑形态被误改", alias)
		}
	}
}
