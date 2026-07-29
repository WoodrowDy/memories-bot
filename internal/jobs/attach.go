package jobs

import (
	"fmt"
	"path"
	"strings"
)

// MaxBytes is the largest attachment the bot will fetch.
//
// 128KB면 한글 6만 자쯤 — 위키에서 제일 긴 노트의 열 배가 넘는다. 이 위로 가는 건
// 노트가 아니라 사고다: 잘못 붙인 파일이거나, 통째로 내보낸 볼트거나.
//
// 상한이 여기 있는 건 두 가지 이유에서다. 프롬프트에 적으면 모델이 봐주기 나름이 되고,
// 첨부는 그대로 모델 입력이 되니까 파일 크기가 곧 한 번의 요금이다. 128KB짜리 초안
// 하나면 입력 토큰이 5만쯤 되고, 그게 이 봇이 한 번에 쓰는 돈의 상한이다.
const MaxBytes = 128 << 10

// File is a markdown attachment: what the gateway chose, and what the worker
// fetches. 슬랙이 알려준 것 중 다운로드에 필요한 것만 남긴다.
type File struct {
	Name string `json:"name"`
	URL  string `json:"url"` // Slack's url_private — 봇 토큰이 있어야 열린다
	Size int    `json:"size"`
}

// Pick chooses the one attachment to fetch and explains every one it passed over.
//
// 왜 하나만 받나: 노트 하나에 본문은 하나다. 파일 두 개를 이어 붙이면 그 순서를 봇이
// 정하게 되는데, 그건 "본문은 우드로가 쓴 그대로"라는 약속 밖의 판단이다.
//
// 왜 조용히 버리지 않나: 던진 사람은 던진 걸 안다. 아무 말 없이 하나만 처리하면
// 나머지가 어디로 갔는지는 PR diff를 열어봐야 알게 된다.
func Pick(files []File) (*File, []string) {
	var chosen *File
	var ignored []string

	for _, f := range files {
		name := f.Name
		if name == "" {
			name = "이름 없는 파일"
		}
		switch {
		case !isMarkdown(f.Name):
			ignored = append(ignored, fmt.Sprintf("%s — `.md`가 아니라 안 읽었어요", name))
		case f.Size > MaxBytes:
			ignored = append(ignored, fmt.Sprintf("%s — %s라 안 읽었어요 (상한 %s)",
				name, human(f.Size), human(MaxBytes)))
		case chosen != nil:
			ignored = append(ignored, fmt.Sprintf("%s — 한 번에 한 개만 올려요", name))
		default:
			f := f // 슬라이스 원소 주소를 그대로 내보내지 않는다
			chosen = &f
		}
	}
	return chosen, ignored
}

// isMarkdown decides on the filename, not on Slack's filetype field.
//
// 슬랙은 같은 .md를 업로드 경로에 따라 markdown으로도 text로도 부르고, 붙여넣기로
// 만들어진 스니펫은 아예 다른 이름이 붙는다. 이름 끝 글자는 우드로가 정한 거라
// 그쪽이 믿을 만하다.
func isMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

func human(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}
