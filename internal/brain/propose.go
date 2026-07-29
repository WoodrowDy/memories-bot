package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/WoodrowDy/memories-wiki-bot/internal/llm"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wiki"
	"github.com/WoodrowDy/memories-wiki-bot/internal/wikiwrite"
)

// Writer is the write surface. It is a separate interface from Wiki so that the
// read path has no way to reach a write credential, and so a bot with no write
// token simply never offers the tool.
type Writer interface {
	Enabled() bool
	Propose(ctx context.Context, p wikiwrite.Proposal) (wikiwrite.Result, error)
}

const maxAlsoFiles = 3

// The wiki's own vocabulary. Validated in code because a status outside this
// set silently breaks every "위키 현황" count from then on.
var validStatus = map[string]bool{
	"seedling": true, "growing": true, "evergreen": true, "archived": true,
}

// CONVENTIONS.md: 파일·폴더는 영문 kebab-case, 공백·한글 파일명 금지.
var kebabFile = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.md$`)

type alsoIn struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Why     string `json:"why"`
}

type proposeIn struct {
	Path    string   `json:"path"`
	Mode    string   `json:"mode"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Tags    []string `json:"tags"`
	Aliases []string `json:"aliases"`
	Summary string   `json:"summary"`
	Also    []alsoIn `json:"also"`

	// BodyFrom names the first line of the real draft, used only to cut off an
	// instruction typed above it. It is not the body.
	BodyFrom string `json:"body_from"`

	// Body is no longer an input. The field is kept so that a model which sends
	// one anyway gets told why it was ignored instead of silently having its
	// prose dropped on the floor.
	Body string `json:"body"`
}

func proposeToolDef() llm.Tool {
	return llm.Tool{
		Name: "propose_note",
		Description: "정리한 노트를 브랜치에 올리고 PR을 연다. 머지는 사람이 하므로 이 툴은 위키를 바로 바꾸지 않는다. " +
			"**우드로가 만들라고 했을 때만 부른다** — \"만들어라\", \"올려줘\", \"넣어줘\", \"PR\", \"ㄱㄱ\", \"그래\" 같은 말이 " +
			"초안과 함께 오지 않았으면 이 툴은 코드에서 거부된다. " +
			"초안만 왔을 땐 자리·프론트매터·status를 슬랙으로 추천하고 멈춰라. " +
			"초안 맨 위에 ---로 감싼 프론트매터가 붙어 있으면 우드로가 정해준 값이니 그대로 옮겨 담아라. " +
			"본문은 네가 쓰지 않는다 — 우드로가 보낸 원문이 그대로 본문이 되고, 이 툴에는 body 칸이 없다. " +
			"네가 정하는 건 어디에 둘지(path·mode)와 어떻게 분류할지(title·status·tags·aliases)뿐이다. " +
			"부르기 전에 반드시 search_wiki로 같은 주제 노트가 있는지 확인하고, " +
			"topics/README.md와 CONVENTIONS.md를 read_note로 읽어 카테고리를 골라라.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type": "string",
					"description": "노트 경로. 새 노트는 영문 kebab-case (예: 'topics/cs/grpc.md'). " +
						"한글·공백 파일명은 거부된다.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"create", "update"},
					"description": "새 노트면 create, 이미 있는 노트에 붙이면 update. 실제 존재 여부와 다르면 거부된다.",
				},
				"title": map[string]any{"type": "string", "description": "노트 제목 (프론트매터 title)"},
				"status": map[string]any{
					"type": "string",
					"enum": []string{"seedling", "growing", "evergreen", "archived"},
					"description": "새 노트는 초안 내용을 보고 seedling이나 growing 중에 고른다 — 직접 해본 자국(돌려본 코드, " +
						"실행 결과나 에러, \"해보니 ~였다\")이 글 안에 있으면 growing, 이해한 걸 적은 글이면 seedling. " +
						"evergreen과 archived는 시간이 지나야 아는 것이라 새 노트에는 거부된다. " +
						"update면 비워두는 게 기본 — 기존 값을 그대로 지킨다. 성숙도는 올릴 수만 있고 내릴 수는 없다(내리는 값은 무시된다).",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "네임스페이스 태그 (예: ['cs/grpc', 'cs/network']). topics/ 노트는 최소 1개.",
				},
				"aliases": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "한/영 검색용 다른 이름. 없으면 생략.",
				},
				"body_from": map[string]any{
					"type": "string",
					"description": "우드로가 보낸 글에서 본문이 시작되는 첫 줄을 그대로 옮겨 적는다. " +
						"'이거 정리해줘' 같은 지시문이 초안 앞에 붙어 있을 때만 쓰고, 아니면 생략한다 " +
						"(생략하면 받은 글 전체가 본문). 요약하거나 고쳐 쓰는 자리가 아니다 — 첫 줄을 그대로 베끼는 자리다.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "어디에 왜 넣었는지 한두 문장. PR 본문과 슬랙 답변에 그대로 쓰인다.",
				},
				"also": map[string]any{
					"type": "array",
					"description": "같은 PR에 담을 다른 파일. 새 노트를 만들면 그 카테고리 README.md 목차에 링크를 더할 때 쓴다. " +
						"content는 그 파일의 전체 내용(부분 수정 아님).",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path":    map[string]any{"type": "string"},
							"content": map[string]any{"type": "string"},
							"why":     map[string]any{"type": "string"},
						},
						"required": []string{"path", "content"},
					},
				},
			},
			"required": []string{"path", "mode", "title", "summary"},
		},
	}
}

type proposeOut struct {
	OK       string   `json:"ok"`
	PRURL    string   `json:"pr_url"`
	Number   int      `json:"pr_number"`
	Branch   string   `json:"branch"`
	Mode     string   `json:"mode"`
	Files    []string `json:"files"`
	Reminder string   `json:"reminder"`
}

// runPropose opens the PR. draft is 우드로's original Slack message — the note
// body comes from there, never from the model.
func (b *Brain) runPropose(ctx context.Context, input json.RawMessage, draft string) (string, error) {
	if b.writer == nil || !b.writer.Enabled() {
		return "", fmt.Errorf("쓰기가 꺼져 있어요 (GITHUB_WRITE_TOKEN 없음). 정리 결과를 글로만 답해주세요")
	}

	var in proposeIn
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("propose_note 인자를 못 읽었어요: %v", err)
	}
	if err := validatePropose(in); err != nil {
		return "", err
	}

	body := draftBody(draft, in.BodyFrom)
	if body == "" {
		return "", fmt.Errorf("본문으로 쓸 원문이 없어요. 이 툴은 우드로가 보낸 글을 그대로 본문에 넣어요 — " +
			"초안 없이 노트를 지어낼 수는 없어요. \"만들어라\"만 온 거라면 봇은 앞 메시지를 못 읽으니, " +
			"초안을 \"만들어라\"와 같은 메시지에 붙여 다시 보내달라고 슬랙에 답해주세요")
	}

	// The model's claim about create-vs-update is checked against the repo, not
	// trusted. Getting this wrong is how a note gets silently replaced.
	prev, exists, err := b.lookup(ctx, in.Path)
	if err != nil {
		return "", err
	}
	switch {
	case in.Mode == "create" && exists:
		return "", fmt.Errorf("%s는 이미 있어요. 그 노트를 read_note로 읽고 mode=\"update\"로 다시 불러주세요", in.Path)
	case in.Mode == "update" && !exists:
		return "", fmt.Errorf("%s는 아직 없어요. mode=\"create\"로 다시 불러주세요", in.Path)
	}
	if in.Mode == "update" {
		body = mergeBody(prev.Body, body)
	}

	files := []wikiwrite.File{{
		Path:    in.Path,
		Content: renderNote(in, prev, body, b.today()),
	}}
	for _, a := range in.Also {
		content, err := b.withFrontmatter(ctx, a)
		if err != nil {
			return "", err
		}
		files = append(files, wikiwrite.File{Path: a.Path, Content: content})
	}

	res, err := b.writer.Propose(ctx, wikiwrite.Proposal{
		Slug:  in.Path,
		Title: prTitle(in),
		Body:  prBody(in, files),
		Files: files,
	})
	if err != nil {
		return "", err
	}

	return encode(proposeOut{
		OK: "PR을 열었어요", PRURL: res.PRURL, Number: res.Number,
		Branch: res.Branch, Mode: in.Mode, Files: res.Files,
		Reminder: "머지는 사람이 합니다. 슬랙 답변에 이 pr_url을 그대로 넣으세요.",
	})
}

// withFrontmatter restores the frontmatter of a file the model rewrote whole.
//
// `also` is how a category README's table of contents gets a link to the new
// note, and the model supplies the README's entire new content. But read_note
// strips the frontmatter before the model ever sees it (wiki.parseNote parks it
// in Note.Frontmatter, not Note.Body), so what comes back has none — and
// writing that raw deleted `title:`, `created:` and `tags:` off topics/cs/README.md
// in PR #1. Asking the model to preserve something it was never shown is not a
// prompt fix; the block is reattached here, from the file itself.
func (b *Brain) withFrontmatter(ctx context.Context, a alsoIn) (string, error) {
	prev, exists, err := b.lookup(ctx, a.Path)
	if err != nil {
		return "", err
	}
	if !exists || prev.Frontmatter == "" {
		// A brand-new file, or one that genuinely has no frontmatter. Nothing to
		// preserve, so whatever the model wrote stands.
		return a.Content, nil
	}
	// If the model invented a frontmatter block of its own it is dropped: the
	// file's real one is the source of truth for created:, tags: and the rest,
	// and two blocks would break the parser.
	body := strings.TrimSpace(stripFrontmatter(a.Content))
	return bumpUpdated(prev.Frontmatter, b.today()) + "\n\n" + body + "\n", nil
}

var updatedRe = regexp.MustCompile(`(?m)^updated:.*$`)

// bumpUpdated moves the updated: date, if the block has one. A README whose
// date never changes reads as abandoned exactly while it is being maintained.
func bumpUpdated(fm, today string) string {
	return updatedRe.ReplaceAllStringFunc(fm, func(string) string { return "updated: " + today })
}

// lookup reports whether the note already exists on the base branch.
func (b *Brain) lookup(ctx context.Context, path string) (*wiki.Note, bool, error) {
	n, err := b.wiki.ReadNote(ctx, path)
	if err != nil {
		// ReadNote fails both for "missing" and for "GitHub is down". Treating a
		// transient failure as "missing" would create a duplicate note, so only
		// a clear 404 counts as absent.
		if isMissing(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%s의 현재 상태를 확인하지 못했어요: %v", path, err)
	}
	return &n, true, nil
}

func isMissing(err error) bool {
	s := err.Error()
	return strings.Contains(s, "404") || strings.Contains(s, "없는 노트")
}

func validatePropose(in proposeIn) error {
	if in.Mode != "create" && in.Mode != "update" {
		return fmt.Errorf("mode는 create 또는 update여야 해요 (받은 값: %q)", in.Mode)
	}
	if !wiki.IsWritablePath(in.Path) {
		return fmt.Errorf("쓸 수 없는 경로예요: %q — topics/ daily/ personal/ projects/ 아래 .md만 돼요 "+
			"(CONVENTIONS.md와 docs/note-style.md는 읽기 전용이에요)", in.Path)
	}
	if strings.HasPrefix(in.Path, "topics/") {
		name := in.Path[strings.LastIndex(in.Path, "/")+1:]
		if !kebabFile.MatchString(name) {
			return fmt.Errorf("파일명은 영문 kebab-case여야 해요 (받은 값: %q). 한글·공백·대문자는 안 돼요", name)
		}
		if in.Mode == "create" && len(in.Tags) == 0 {
			return fmt.Errorf("topics/ 노트에는 태그가 최소 하나 필요해요 (예: [\"cs/grpc\"])")
		}
	}
	if in.Status != "" && !validStatus[in.Status] {
		return fmt.Errorf("status는 seedling/growing/evergreen/archived 중 하나예요 (받은 값: %q)", in.Status)
	}
	if in.Mode == "create" && in.Status == "" {
		return fmt.Errorf("새 노트에는 status가 필요해요 — 초안을 보고 seedling이나 growing 중에 골라주세요")
	}
	// docs/note-style.md §6: evergreen은 "다시 열었을 때 고칠 게 없었다"는 판정이고
	// archived는 "더 안 본다"는 결정이다. 둘 다 시간이 지나야 알 수 있는 것이라 방금
	// 만든 노트에는 논리적으로 붙을 수 없다 — 초안 한 편을 읽고 내릴 수 있는 판단이
	// 아니다. 승급은 우드로가 위키에서 한다.
	if in.Mode == "create" && (in.Status == "evergreen" || in.Status == "archived") {
		return fmt.Errorf("새 노트는 seedling이나 growing까지예요 (받은 값: %q). "+
			"evergreen은 다시 열어봐야 알고 archived는 그만 볼 때 붙는 거라, 그 둘은 우드로가 위키에서 직접 올려요", in.Status)
	}
	if strings.TrimSpace(in.Title) == "" {
		return fmt.Errorf("title이 비었어요")
	}
	// Refused rather than ignored: a model that wrote out a whole body and had
	// it silently discarded would keep doing it, and would keep paying for it.
	if strings.TrimSpace(in.Body) != "" {
		return fmt.Errorf("body는 안 받아요 — 본문은 우드로가 보낸 원문이 그대로 들어가요. " +
			"path·mode·title·status·tags·aliases·summary만 주고, 다듬을 점은 파일이 아니라 슬랙 답변에 적어주세요")
	}
	if strings.TrimSpace(in.Summary) == "" {
		return fmt.Errorf("summary가 비었어요 — 어디에 왜 넣었는지 한두 문장 주세요")
	}
	if len(in.Also) > maxAlsoFiles {
		return fmt.Errorf("also는 최대 %d개예요 (받은 값: %d)", maxAlsoFiles, len(in.Also))
	}
	for _, a := range in.Also {
		if !wiki.IsWritablePath(a.Path) {
			return fmt.Errorf("also에 쓸 수 없는 경로가 있어요: %q", a.Path)
		}
		if a.Path == in.Path {
			return fmt.Errorf("also에 노트 자기 자신(%q)을 또 넣지 마세요", a.Path)
		}
		if strings.TrimSpace(a.Content) == "" {
			return fmt.Errorf("also의 %s 내용이 비었어요 (전체 내용을 주세요)", a.Path)
		}
	}
	return nil
}

// ---- the body comes from 우드로, not from the model ----
//
// The model used to write the note body: it read note-style.md and re-emitted
// the draft in that shape. That cost a tool argument as long as the note, which
// put every draft under the model's output ceiling — a long enough one came
// back as truncated JSON, and the tidy failed outright.
//
// It also solved a problem 우드로 doesn't have. He writes the notes; what he
// wanted was to be told *where* one goes and *how* it is filed. So the draft is
// spliced in here, verbatim, and the model's whole output is a handful of
// metadata fields. Draft length stops mattering, nothing is paraphrased on the
// way in, and what the model notices about the writing goes into the Slack
// reply where he can act on it — instead of into the file where he'd have to
// diff it back out.

// ---- 확인이 먼저, PR은 그다음 ----
//
// 초안만 던지면 봇은 자리와 status를 말해주고 멈춘다. PR은 "올려줘"라고 했을 때만
// 열린다. 그 판정이 여기 있는 이유는 프롬프트가 규칙이 아니기 때문이다 — 모델이 한
// 번 헷갈릴 때마다 PR이 하나씩 열리게 둘 수는 없다.
//
// 승인은 초안과 같은 메시지를 타고 온다. 스레드로 나눠 받으려면 봇이 자기 스레드의
// 원문을 읽을 수 있어야 하는데(conversations.replies), 그건 슬랙 앱 권한을 하나 더
// 받고 다시 설치해야 하는 일이라 지금은 하지 않는다.

// goAheadWords are the ways 우드로 says "do it", matched anywhere in a short
// line so "이거 정리해서 올려줘"처럼 앞에 말이 붙어도 걸린다.
//
// 세 낱말은 뒤를 묶어뒀다. pr에 단어 경계가 없으면 Proxy·Prototype이 적힌 줄이 PR을
// 열고, 만들어·넣어 뒤를 열어두면 "객체를 만들어 반환한다"나 "리스트에 넣어 두면" 같은
// 초안 문장이 PR을 연다.
var goAheadWords = regexp.MustCompile(`(?i)올려|올리자|올려놔|피알|\bpr\b|ㄱㄱ|고고|가자|` +
	`만들어(?:줘|주세요|라)|만들자|생성해줘|열어줘|넣어(?:줘|주세요|라)`)

// goAheadOnly matches a line that is *nothing but* the instruction — "그래",
// "동의", "이대로 PR 올려줘!", "맞춰서 넣어줘". 줄 전체를 걸기 때문에 같은 낱말이
// 문장 속에 들어 있으면 걸리지 않는다. 짧은 대답 하나가 승인이 되는 건 여기서만 허용한다.
//
// 앞머리는 닫힌 목록이고 두 낱말까지만 받는다. 아무 말이나 앞에 오게 열어두면
// "이건 나중에 올려야 한다" 같은 본문 문장이 통째로 지워지고, 그건 diff를 열어봐야
// 보인다. 목록에 든 낱말은 전부 그 자체로 지시어라 본문에 홀로 설 일이 없다.
var goAheadOnly = regexp.MustCompile(`(?i)^(?:(?:이대로|이거|이건|그거|그럼|그래|자|응|넵|네|ㅇㅇ|좋아|오케이|ok|` +
	`맞춰서|맞춰|정리해서|정리해)[\s,]*){0,2}` +
	`(?:노트\s*)?(?:pr|피알)?\s*` +
	`(?:올려(?:줘|줘요|주세요|라|도\s*돼)?|올리자|올려놔|열어(?:줘|주세요)?|만들어(?:줘|주세요|라|도\s*돼)?|만들자|` +
	`넣어(?:줘|주세요|라|도\s*돼)?|` +
	`생성(?:해줘|해)?|동의|ㄱㄱ|고고|가자|콜|ok|오케이|좋아|그래|응|넵|네|ㅇㅇ)` +
	`\s*(?:pr|피알)?\s*[.!?~ㅋㅎ\s]*$`)

// instructionRunes is how long a line can be and still be an instruction rather
// than writing. 지시문은 짧다. 문단은 지시문이 아니다.
const instructionRunes = 30

// goAhead reports whether 우드로 asked for the PR or only for a look.
//
// 지시문은 글의 맨 위나 맨 아래에 붙는다. 가운데를 보지 않는 건 "이건 나중에 올려야
// 한다" 같은 본문 한 줄이 PR을 열지 않게 하려는 것이고, 길이와 마크다운 기호를 보는
// 것도 같은 이유다.
//
// 두 번 보는 이유는 펜스다. 초안을 ```로 감싸 보내면 맨 아래 줄이 ```가 되어버려서
// 그 안에 쓴 승인이 가장자리에서 사라진다. 밖에서 못 찾으면 펜스를 벗기고 한 번 더 본다.
func goAhead(text string) bool {
	if instructionAtEdge(text) {
		return true
	}
	if inner, fenced := unfence(text); fenced {
		return instructionAtEdge(inner)
	}
	return false
}

func instructionAtEdge(text string) bool {
	lines := strings.Split(text, "\n")
	first, last := firstContent(lines), lastContent(lines)
	if first < 0 {
		return false
	}
	return isInstruction(lines[first]) || isInstruction(lines[last])
}

func isInstruction(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" || len([]rune(line)) > instructionRunes {
		return false
	}
	switch line[0] {
	case '-', '*', '#', '>', '|', '+': // 목록·헤딩·인용·표는 글이지 지시문이 아니다
		return false
	}
	// 두 갈래인 이유: 앞뒤에 말이 붙어도 알아보려면 낱말 검색이 필요하고("정리해서
	// 올려줘"), "그래"·"동의"처럼 그 자체로 승인인 짧은 대답은 줄 전체를 걸어야만
	// 본문 속 같은 낱말과 구별된다.
	return goAheadWords.MatchString(line) || goAheadOnly.MatchString(line)
}

// stripGoAheadLine removes a line that is nothing but "올려줘".
//
// 승인이 초안과 한 메시지에 실려 오니, 걷어내지 않으면 그 말이 노트에 그대로 남는다.
// body_from은 초안 *위에* 붙은 지시문만 잘라낸다 — 앞을 자르는 도구라 아래에 붙은
// 줄에는 닿지 못한다.
//
// 판정은 goAhead보다 엄격하게, 줄 전체를 걸어서 한다. 둘이 어긋나는 방향은 골라둔
// 것이다: 승인을 못 알아보면 봇이 추천만 하고 우드로가 한 번 더 치면 그만이지만,
// 넉넉히 지우면 그가 쓴 문장이 노트에서 사라지고 그건 diff를 들여다봐야 보인다.
// 그래서 알아보는 쪽은 넓게, 지우는 쪽은 좁게 잡는다.
func stripGoAheadLine(s string) string {
	lines := strings.Split(s, "\n")
	if i := firstContent(lines); i >= 0 && goAheadOnly.MatchString(strings.TrimSpace(lines[i])) {
		lines = lines[i+1:]
	}
	if i := lastContent(lines); i >= 0 && goAheadOnly.MatchString(strings.TrimSpace(lines[i])) {
		lines = lines[:i]
	}
	return strings.Join(lines, "\n")
}

// firstContent and lastContent are the indexes of the outermost non-blank lines.
func firstContent(lines []string) int {
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			return i
		}
	}
	return -1
}

func lastContent(lines []string) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return -1
}

// draftBody turns the raw Slack text into a note body.
//
// Only four things are removed, all artefacts of *sending* rather than content:
// the go-ahead word that unlocked the PR, the ``` block 우드로 wraps a draft in so
// Slack stops eating its markdown, a frontmatter block copied out of Obsidian
// (renderNote writes its own, and two would break the parser), and an
// instruction typed above the draft, which the model marks by naming the first
// line of the real content.
//
// Nothing else. 문장도, 빈 줄도, 헤딩 레벨도 그대로 간다 — 본문은 우드로가 쓴 글이다.
func draftBody(draft, from string) string {
	// 승인은 펜스 밖에 붙기도 하고 안에 들어가기도 한다. 밖을 먼저 걷어내고, 밖에서
	// 걷어낸 게 없을 때만 안쪽을 본다. 두 번 다 지우면 마침 승인 낱말로 끝나는 본문
	// 마지막 줄까지 같이 사라지는데, 그건 diff를 열어봐야 보인다.
	body := stripGoAheadLine(draft)
	tookOutside := body != draft

	// 위에 붙은 지시문을 펜스보다 먼저 잘라낸다. 지시문은 펜스 *밖에* 붙으니 남겨두면
	// 여는 ```가 첫 줄이 아니게 되고, 그러면 unfence가 짝을 못 찾아 노트 위아래에
	// ```가 그대로 박힌다.
	if from = firstLine(from); from != "" {
		if i := findLineWithPrefix(body, from); i >= 0 {
			body = body[fenceAbove(body, i):]
		}
		// Not found: the model paraphrased instead of copying. Keeping the whole
		// draft leaves a stray "이거 정리해줘" at the top of the note, which shows
		// in the diff — better than cutting at a guess and losing real content.
	}

	if inner, fenced := unfence(body); fenced {
		body = inner
		if !tookOutside {
			body = stripGoAheadLine(body)
		}
	}
	body = stripFrontmatter(body)
	return strings.TrimSpace(body)
}

// unfence removes the ``` block a whole draft was pasted inside.
//
// 슬랙 입력창은 마크다운을 먹는다 — "- "는 서식 불릿이 되고 "#"은 사라진다. 그래서
// 우드로는 초안을 코드블록에 넣어 보내고, 그러면 ```가 메시지 원문에 그대로 남아
// 노트 위아래에 박힌다. 보내느라 생긴 자국이지 그가 쓴 글이 아니라서 걷어낸다.
//
// 여는 줄의 언어 태그는 닫힌 목록이다. 여기서 잘못 벗기면 코드블록으로 시작하고
// 코드블록으로 끝나는 노트의 첫 줄과 끝 줄이 조용히 날아가는데, 통째로 감쌀 때 붙는
// 태그는 없거나 md 계열이고 코드블록을 여는 노트는 ```go처럼 언어를 적는다. 그
// 차이가 유일하게 믿을 만한 표시다 — 알아보는 쪽은 넓게, 지우는 쪽은 좁게.
var fenceOpen = regexp.MustCompile("^```(?:md|markdown|text|txt)?$")

func unfence(s string) (string, bool) {
	lines := strings.Split(s, "\n")
	first, last := firstContent(lines), lastContent(lines)
	if first < 0 || last <= first {
		return s, false
	}
	if !fenceOpen.MatchString(strings.TrimSpace(lines[first])) {
		return s, false
	}
	if strings.TrimSpace(lines[last]) != "```" {
		return s, false
	}
	return strings.Join(lines[first+1:last], "\n"), true
}

// stripFrontmatter drops a leading --- ... --- block, but only if it looks like
// frontmatter. A note may legitimately open with --- as a horizontal rule, and
// cutting to the next --- would eat the first section.
func stripFrontmatter(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	rest, ok := cutPrefixLine(s, "---")
	if !ok {
		return s
	}
	for off := 0; off < len(rest); {
		line, next := lineAt(rest, off)
		switch {
		case strings.TrimSpace(line) == "---":
			return rest[next:]
		case !frontmatterKey.MatchString(line):
			return s // a non-key line: this was never frontmatter
		}
		off = next
	}
	return s // opened but never closed
}

var frontmatterKey = regexp.MustCompile(`^\s*([A-Za-z_][\w-]*\s*:|-\s)`)

// findLineWithPrefix returns the offset of the first line starting with want.
func findLineWithPrefix(s, want string) int {
	for off := 0; off < len(s); {
		line, next := lineAt(s, off)
		if t := strings.TrimSpace(line); t != "" && strings.HasPrefix(t, want) {
			return off
		}
		off = next
	}
	return -1
}

// fenceAbove moves a cut point up over the ``` line that opens the block the
// target sits inside.
//
// 초안을 코드블록에 넣고 그 위에 "이번주에 공부한 건데 올려줘" 한 줄을 적으면, 모델이
// 짚어주는 본문 첫 줄은 여는 ``` *아래*에 있다. 거기서 그대로 자르면 여는 줄만
// 사라지고 닫는 ```는 노트 끝에 남는다.
//
// 자르는 자리를 한 줄 위로 올릴 뿐이라 남는 글은 늘어나기만 한다 — 그 ```를 벗길지
// 말지는 unfence가 제 규칙으로 다시 판단한다. 그래서 ```go처럼 진짜 코드블록으로
// 시작하는 초안이 걸려도 잃는 건 없다.
func fenceAbove(s string, at int) int {
	head := strings.TrimRight(s[:at], " \t\r\n")
	if head == "" {
		return at
	}
	start := strings.LastIndexByte(head, '\n') + 1
	if !strings.HasPrefix(strings.TrimSpace(head[start:]), "```") {
		return at
	}
	return start
}

// mergeBody folds a draft into a note that already exists.
//
// Append, never replace. A replace that guesses wrong destroys writing that
// exists nowhere else; an append that guesses wrong leaves the new material in
// the wrong place — which the PR diff shows plainly and 우드로 fixes by dragging
// it. One mistake is recoverable and the other is not, and that asymmetry is
// the whole argument.
func mergeBody(prev, add string) string {
	prev = strings.TrimSpace(prev)
	add = strings.TrimSpace(add)
	if prev == "" {
		return add
	}
	// A pasted draft usually repeats the note's title as an H1. Left in, it
	// would sit mid-file as a second document heading.
	if hasH1(prev) {
		add = dropLeadingH1(add)
	}
	// note-style.md: 섹션 사이는 ---로 끊는다. It also makes the seam obvious in
	// the diff, which is where 우드로 decides whether the placement is right.
	return prev + "\n\n---\n\n" + add
}

func hasH1(s string) bool {
	for off := 0; off < len(s); {
		line, next := lineAt(s, off)
		if strings.HasPrefix(line, "# ") {
			return true
		}
		off = next
	}
	return false
}

func dropLeadingH1(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	rest, ok := cutPrefixLine(s, "# ")
	if !ok {
		return s
	}
	return strings.TrimLeft(rest, " \t\r\n")
}

// lineAt returns the line starting at off and the offset of the next one.
func lineAt(s string, off int) (line string, next int) {
	i := strings.IndexByte(s[off:], '\n')
	if i < 0 {
		return strings.TrimRight(s[off:], "\r"), len(s)
	}
	return strings.TrimRight(s[off:off+i], "\r"), off + i + 1
}

// cutPrefixLine reports whether s opens with a line starting with prefix, and
// returns what follows that line.
func cutPrefixLine(s, prefix string) (rest string, ok bool) {
	line, next := lineAt(s, 0)
	if prefix == "---" {
		if strings.TrimSpace(line) != "---" {
			return s, false
		}
	} else if !strings.HasPrefix(line, prefix) {
		return s, false
	}
	return s[next:], true
}

func firstLine(s string) string {
	line, _ := lineAt(strings.TrimLeft(s, " \t\r\n"), 0)
	return strings.TrimSpace(line)
}

// renderNote builds the file: frontmatter in the wiki's key order, then body.
//
// body is passed in rather than read off in because it is 우드로's text, not the
// model's — see draftBody above.
//
// On update the previous frontmatter is merged rather than replaced — created
// is preserved, tags and aliases are unioned, and an omitted status keeps the
// old one so a tidy-up can never quietly demote an evergreen note to seedling.
func renderNote(in proposeIn, prev *wiki.Note, body, today string) string {
	title, status := in.Title, in.Status
	tags, aliases := in.Tags, in.Aliases
	created := today

	if prev != nil {
		if title == "" {
			title = prev.Title
		}
		status = keepMature(prev.Status, status)
		if prev.Created != "" {
			created = prev.Created
		}
		tags = union(prev.Tags, tags)
		aliases = union(prev.Aliases, aliases)
	}
	if status == "" {
		status = "seedling"
	}

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	if len(aliases) > 0 {
		fmt.Fprintf(&b, "aliases: [%s]\n", strings.Join(aliases, ", "))
	}
	fmt.Fprintf(&b, "created: %s\n", created)
	fmt.Fprintf(&b, "updated: %s\n", today)
	if len(tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(tags, ", "))
	}
	fmt.Fprintf(&b, "status: %s\n", status)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(body))
	b.WriteString("\n")
	return b.String()
}

// statusRank orders the maturity ladder. archived sits at 0 because it is not a
// rung — it is off the ladder, and a note coming back from it is starting over.
var statusRank = map[string]int{"archived": 0, "seedling": 1, "growing": 2, "evergreen": 3}

// keepMature resolves the status on an update.
//
// status is a claim about how well a note has settled, and a tidy-up is no
// evidence that it settled *less* — so the bot may raise a status but never
// lower one. Left to itself the model writes "seedling" out of habit (that is
// what every new note gets), which would quietly knock an evergreen note back
// down every time a draft was folded into it.
//
// Archiving is excluded for a different reason: deciding a note is finished is
// 우드로's call, made in the wiki, not a side effect of a PR that was only
// supposed to add material.
func keepMature(prev, next string) string {
	if next == "" || next == "archived" {
		return prev
	}
	if statusRank[next] < statusRank[prev] {
		return prev
	}
	return next
}

// union keeps the existing order and appends what is new, case-insensitively.
func union(old, add []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(old)+len(add))
	for _, s := range append(append([]string{}, old...), add...) {
		s = strings.TrimSpace(s)
		k := strings.ToLower(s)
		if s == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func prTitle(in proposeIn) string {
	if in.Mode == "update" {
		return fmt.Sprintf("노트 보강: %s", in.Title)
	}
	return fmt.Sprintf("노트: %s", in.Title)
}

func prBody(in proposeIn, files []wikiwrite.File) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(in.Summary))
	b.WriteString("\n\n### 담긴 파일\n")
	for i, f := range files {
		why := ""
		if i > 0 && i-1 < len(in.Also) {
			why = in.Also[i-1].Why
		} else if in.Mode == "update" {
			why = "기존 노트 뒤에 초안을 이어붙임"
		} else {
			why = "새 노트"
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", f.Path, why)
	}
	// Said in the PR, not just in the code: a reviewer who thinks the bot
	// rewrote the prose will read the diff differently than one who knows it
	// didn't.
	b.WriteString("\n본문은 보낸 원문 그대로입니다 — 봇은 자리(경로·카테고리)와 " +
		"분류(status·tags·aliases)만 정했습니다. 문장은 손대지 않았습니다.\n")
	b.WriteString("\n---\n🤖 `memories-wiki-bot`이 연 PR입니다. 내용을 확인하고 머지해주세요.\n")
	return b.String()
}
