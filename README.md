# memories-wiki-bot

Slack에서 `@봇 요즘 뭐 정리했지?` 하면 내 GitHub 위키([`memories`](https://github.com/WoodrowDy/memories))를 직접 뒤져서 답하는 봇.
**읽기 전용** — 아직 쓰기(커밋)는 하지 않는다.

| 단계 | 내용 | 상태 |
|---|---|---|
| 1단계 | 키워드 검색 — 멘션하면 노트를 찾아 경로·요약을 답한다 | ✅ 완료 |
| 2단계 | 자연어 브레인 — 모델이 툴을 골라 목록·본문을 읽고 답한다 | ✅ 2026-07-27 배포 |
| 3단계 | 쓰기 — 붙여넣은 글을 규칙대로 정리해 PR로 올린다 | 다음 ([아래](#다음-단계--3단계-쓰기)) |
| 이후 | 검색 품질, MCP 분리 | 미착수 |

## 지금 되는 것

```
Slack 멘션
  → API Gateway → gateway 람다 (서명 검증 → SQS에 넣고 즉시 200)
                     ↓ SQS
                  worker 람다 → Claude 툴 콜링 루프
                                 ├ search_wiki  키워드로 노트 찾기
                                 ├ list_notes   목록 훑기
                                 ├ read_note    본문 읽기
                                 └ wiki_status  전체 통계
                              → 스레드에 답글
```

- `@봇 동시성 정리한 거 있어?` → 검색 후 경로·status·요약·링크
- `@봇 요즘 뭐 배웠어?` → 목록을 훑고 최근 것들을 골라 요약
- `@봇 카프카 노트 요약해줘` → 본문을 실제로 읽고 요약
- `@봇 위키 현황` → 카테고리·성숙도·daily/personal 통계

**아직 안 하는 것**: 쓰기/커밋, 이미지, 여러 사람 권한 분리. (3단계)

**함수가 둘로 갈린 이유가 이 프로젝트의 핵심 제약이다.** 슬랙은 3초 안에 200을 못 받으면
같은 이벤트를 재시도한다. 그런데 모델이 툴을 두세 번 돌려 답을 만드는 데는 5~20초가 걸린다.
큐가 없으면 같은 답이 세 번 달린다. SQS가 그 경계다.
배포되는 바이너리는 하나이고, 들어온 이벤트 모양을 보고 자기가 gateway인지 worker인지 고른다.

### 조용해지지 않는 3중 안전망

| 상황 | 동작 |
|---|---|
| `ANTHROPIC_API_KEY` 없음 | 1차 키워드 검색으로 답함 |
| Anthropic API 실패·과부하 | 키워드 검색으로 폴백 |
| SQS 전송 실패 | gateway가 그 자리에서 답함 (느리지만 답은 감) |

봇이 침묵하는 것이 가장 나쁜 실패다. 어떤 경로가 죽어도 답은 나가게 만들었다.
1단계 키워드 검색은 2단계가 들어왔다고 지워진 게 아니라, 폴백 경로로 그대로 살아 있다.

## 구조

```
cmd/bot/main.go          # 바이너리 하나, 이벤트 모양 보고 gateway/worker 자동 분기
internal/brain/          # 2단계 핵심 — 시스템 프롬프트 + 툴 정의 + 툴 콜링 루프
internal/llm/            # Anthropic Messages API 클라이언트 (stdlib만, 재시도 포함)
internal/queue/          # SQS SendMessage
internal/awssig/         # SigV4 서명 (AWS 공식 테스트 벡터로 검증)
internal/jobs/           # 큐에 실려가는 작업 한 건의 모양
internal/wiki/           # GitHub에서 memories 읽기 + 60초 캐시 + 경로 화이트리스트
internal/render/         # 키워드 폴백용 메시지 렌더링
internal/slackevents/    # 서명 검증 · 이벤트 파싱
internal/slackclient/    # chat.postMessage
internal/audit/          # 감사 로그
serverless.yml           # gateway + worker + SQS + DLQ
```

설계 원칙: **모델은 GitHub를 직접 만지지 않는다.** 네 개의 툴만 부를 수 있고,
각 툴은 자기 인자를 코드에서 검증한다. 특히 `read_note`의 경로는 프롬프트가 아니라
`IsNotePath()` 화이트리스트가 막는다 (`topics/` `daily/` `personal/` `projects/` 아래 `.md`만).

루프에는 뚜껑이 있다: 최대 5턴, 응답 1024토큰, 노트 본문 6000자, worker 동시 실행 2개.

## 비용

기본 모델은 `claude-haiku-4-5` ($1 / $5 per MTok).
질문 하나에 보통 2~3턴, 입력 3~8k · 출력 200~400토큰 → **질의당 약 $0.01 안팎**.
하루 20번 물어도 한 달 몇 달러 수준. 더 어려운 추론이 필요하면 `.env`에
`LLM_MODEL=claude-sonnet-5`를 넣으면 된다.
AWS 쪽(Lambda·SQS·API Gateway)은 이 정도 트래픽이면 프리티어 안에서 논다.

## Slack 앱 만들기 (봇 유저)

> 지금 Cowork에 연결한 'Slack 커넥터'와 **별개**. 이건 채널에 상주할 *네 봇*을 만드는 것.

1. https://api.slack.com/apps → **Create New App** → From scratch. (개인 테스트 워크스페이스 권장)
2. **OAuth & Permissions → Bot Token Scopes**: `app_mentions:read`, `chat:write` 추가 → **Install to Workspace** → `xoxb-...` **Bot User OAuth Token** 확보.
3. **Basic Information → Signing Secret** 확보.
4. **Event Subscriptions → Enable** → Request URL에 배포된 `https://.../slack/events` 입력(서명시크릿 설정 후 검증 통과). **Subscribe to bot events → `app_mention`** 추가 → 저장.
5. 채널에 봇 초대: 대상 채널에서 `/invite @위키봇`.

## 빌드 · 배포

```bash
go vet ./... && go build ./... && go test ./...

cp .env.example .env      # 처음 한 번만 — 시크릿을 채운다

./deploy.sh               # .env 로드 → vet·test 게이트 → arm64 빌드 → deploy
```

`.env`에 채울 것: `SLACK_SIGNING_SECRET`, `SLACK_BOT_TOKEN` (필수),
`ANTHROPIC_API_KEY` (없으면 키워드 모드), `GITHUB_TOKEN` (선택, rate limit 완화).

배포 후 나온 HTTP API URL + `/slack/events`를 4번 Request URL에 등록.
다른 위키를 쓰려면 `WIKI_OWNER` / `WIKI_REPO` / `WIKI_BRANCH` 환경변수로 교체.
자세한 절차와 막혔을 때 확인할 것들은 [DEPLOY.md](DEPLOY.md)에 있다.

> **rate limit:** 공개 repo는 토큰 없이 읽지만, GitHub 무인증 API는 시간당 60회 제한.
> 툴 콜링은 한 질문에 여러 번 위키를 읽으므로 60초 캐시를 둬서 같은 스냅샷을 재사용한다.
> 그래도 한도에 걸리면 `.env`의 `GITHUB_TOKEN=`에 읽기 전용 fine-grained PAT를 채우고
> 재배포하면 5000/시간.

## 다음 단계 — 3단계 (쓰기)

붙여넣은 글을 위키의 [노트 작성 스타일](https://github.com/WoodrowDy/memories/blob/main/docs/note-style.md)대로
정리해서 올린다. 정리 규칙은 봇 안에 하드코딩하지 않고 위키 쪽 문서를 읽혀서 쓴다 —
규칙이 바뀌면 위키만 고치면 되게.

**읽기와 쓰기는 성질이 다르다.** 읽기는 틀려도 답이 이상할 뿐이지만, 쓰기는 위키를
오염시키고 되돌리는 데 사람 손이 든다. 그래서 한 번에 붙이지 않고 네 조각으로 쪼갠다.

### 3a. 쓰기 없이 정리만 (위험 0, 여기부터)

쓰기 권한을 전혀 주지 않은 채로, 정리 **결과를 텍스트로만** 답하게 한다.
슬랙에 초안을 붙이면 "`topics/cs/grpc.md`에, 프론트매터는 이것, 섹션은 이 순서로" 까지 나오고,
파일 만들고 커밋하는 건 손으로. 권한 없이 가치의 대부분을 먼저 가져간다.

지금 이걸 막고 있는 건 두 군데다.

- `internal/wiki/wiki.go`의 `underContent()` — 읽기 화이트리스트가 `topics/ daily/ personal/ projects/`
  넷뿐이라 정작 규칙 원본인 `docs/note-style.md`와 루트 `CONVENTIONS.md`를 못 읽는다. 둘을 더한다.
- `internal/brain/brain.go`의 `maxTokens = 1024` — 노트 전문을 뱉으면 잘린다. 정리 요청일 때만 올린다.

여기에 "정리 요청이면 규칙 문서를 먼저 읽고 경로·프론트매터까지 만들어 답하라"를
시스템 프롬프트에 넣으면 3a는 끝이다. 새 토큰도, 새 스코프도 필요 없다.

### 3b. PR로 올리기

`propose_note` 툴 하나를 더한다.

```
git/ref/heads/main  →  base sha
git/refs            →  bot/note-<slug> 브랜치 생성
contents/<path>     →  파일 생성
pulls               →  PR 열기
```

**PR 자체가 승인 게이트다.** 봇은 열기만 하고 머지는 못 한다. 사람이 머지하지 않으면
`main`은 그대로고, 되돌리기는 "PR 닫기"로 끝난다.

새 파일만 만들 수 있고 기존 파일 덮어쓰기는 코드에서 막는다. 쓰기 토큰은 읽기 토큰과
**분리된 환경변수**(`GITHUB_WRITE_TOKEN`)로 두고, 읽기 클라이언트가 그 값을 볼 수 없게 타입을 나눈다.

### 3c. 슬랙에서 승인

👍 리액션으로 머지. `reactions:read` 스코프와 `reaction_added` 이벤트 구독이 추가로 필요하다.
편의 기능이지 안전장치가 아니다 — 안전장치는 이미 3b의 PR이다.

### 3d. 기존 노트 수정

`propose_edit`. 새로 만드는 것보다 어렵다. 기존 본문을 읽고 어디를 어떻게 바꿀지 정해야 하고,
그 사이 파일이 바뀌었을 가능성(sha 충돌)도 다뤄야 한다. 마지막에 한다.

## 그 이후

- **검색 품질** — 한국어 조사 처리(`동시성을` → `동시성`), 별칭·태그 가중치 조정.
  브레인이 부르는 `search_wiki`가 이걸 쓰므로 폴백뿐 아니라 평상시 답 품질도 같이 오른다.
- **MCP 분리(학습)** — `internal/wiki`를 별도 MCP 서버로 떼면 슬랙 봇뿐 아니라
  Claude 데스크톱에서도 같은 도구를 쓴다. 기능보다 MCP 구조를 익히는 게 목적.

## 안 바꾸는 규칙

1. **모델은 GitHub를 직접 만지지 않는다.** 정해진 툴만 부르고, 각 툴은 자기 인자를 코드에서 검증한다.
2. **권한 검사는 프롬프트가 아니라 코드로.** 프롬프트로 막은 것은 뚫린다고 가정한다.
3. **읽기 툴과 쓰기 툴은 분리한다.** 토큰도, 타입도.
4. **모든 쓰기는 미리보기 + 승인을 거친다.** 무조건 덮어쓰기 금지.
5. **툴은 구조화된 JSON을 돌려주고, 말은 렌더 레이어가 만든다.**
6. **시크릿은 환경변수에만.** 코드에도 위키에도 두지 않는다.
