package brain

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
)

// preview_note is the 확인 round's dry run: the same decision propose_note
// makes, minus the PR.
//
// 확인 라운드에서 모델은 "이 프론트매터를 심을게요"라고 손으로 타이핑했다. 그 블록은
// 모델 *자신의 제안*이었고, 실제로 심기는 값은 코드가 그의 파일과 합쳐서 정한다 —
// pickList에서 그가 적어둔 값이 이긴다. 그래서 2026-07-30 실행에서 그가 옵시디언에서
// 직접 붙인 `cs/layering` 태그가 미리보기에는 없고 PR에는 들어갔다. 보여준 것과 심은
// 것이 달랐던 것이고, 더 나쁜 건 그 블록을 "복사해 초안 맨 위에 붙이세요"라고 안내한
// 것이다 — 그대로 붙였으면 그의 태그가 조용히 사라졌다.
//
// 고치는 방향은 모델에게 더 잘 설명하는 쪽이 아니다. 값을 정하는 코드가 그 값을
// 보여주게 하는 쪽이다.
func previewToolDef() llm.Tool {
	return llm.Tool{
		Name: "preview_note",
		Description: "**확인 모드에서 반드시 부른다.** 그 자리에 실제로 심길 프론트매터를 코드가 계산해서 돌려준다 — " +
			"PR은 열리지 않고 위키도 바뀌지 않는다(읽기만 한다). " +
			"프론트매터를 네가 지어서 적지 마라: 우드로가 초안 맨 위에 적어둔 값은 네 값보다 세고, " +
			"그 합치기는 코드만 안다. 돌려받은 frontmatter를 슬랙 답변의 코드블록에 글자 그대로 옮겨라. " +
			"인자는 propose_note와 같고, 부르기 전에 search_wiki로 같은 주제 노트가 있는지 확인하고 " +
			"topics/README.md와 CONVENTIONS.md를 읽어 카테고리를 골라라. " +
			"**올리는 모드에서는 부르지 마라** — propose_note가 같은 계산을 하고 PR까지 연다.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": noteFields(),
			"required":   []string{"path", "mode", "title", "summary"},
		},
	}
}

type previewOut struct {
	Path        string   `json:"path"`
	Mode        string   `json:"mode"`
	Frontmatter string   `json:"frontmatter"`
	Filled      []string `json:"filled,omitempty"`
	Replaced    []string `json:"replaced,omitempty"`
	Note        string   `json:"note"`
}

// runPreview resolves the frontmatter and stops there. Nothing is written.
//
// runPropose와 같은 순서로 같은 함수들을 부른다 — validatePropose, 그가 적은
// 프론트매터 파싱, lookup, checkMode, resolveMeta, renderFrontmatter. 한 줄이라도
// 갈리면 미리보기는 미리보기가 아니라 별개의 추측이 된다.
func (b *Brain) runPreview(ctx context.Context, input json.RawMessage, ask Ask) (string, error) {
	var in proposeIn
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("preview_note 인자를 못 읽었어요: %v", err)
	}
	if err := validatePropose(in); err != nil {
		return "", err
	}

	// 첨부가 있으면 그 파일 맨 위가 그가 정한 값이다. 여기서 wiki.ParseFrontmatter를
	// 쓰는 이유도 runPropose와 같다 — 되메우기가 없어서 빈 칸이 빈 칸으로 남는다.
	var own *wiki.Note
	if ask.File != nil {
		n := wiki.ParseFrontmatter(ask.File.Content)
		own = &n
	}

	prev, exists, err := b.lookup(ctx, in.Path)
	if err != nil {
		return "", err
	}
	if err := checkMode(in.Mode, in.Path, exists); err != nil {
		return "", err
	}

	m := resolveMeta(in, prev, own, b.today())
	return encode(previewOut{
		Path:        in.Path,
		Mode:        in.Mode,
		Frontmatter: renderFrontmatter(m),
		Filled:      m.Filled,
		Replaced:    m.Replaced,
		Note: "이 frontmatter를 슬랙 답변의 코드블록에 그대로 옮겨 적어요. 한 줄도 고치거나 빼지 마세요 — " +
			"올릴 때 파일에 심기는 게 정확히 이거예요. PR은 열리지 않았어요.",
	})
}
