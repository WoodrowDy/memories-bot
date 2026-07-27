# memories-wiki-bot

Slack에서 `@봇 요즘 뭐 정리했지?` 하면 내 GitHub 위키(`memories`)를 직접 뒤져서 답하는 봇.
**읽기 전용** — 아직 쓰기(커밋)는 하지 않는다.

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

**아직 안 하는 것**: 쓰기/커밋, 이미지, 여러 사람 권한 분리. (다음 단계)

### 조용해지지 않는 3중 안전망

| 상황 | 동작 |
|---|---|
| `ANTHROPIC_API_KEY` 없음 | 1차 키워드 검색으로 답함 |
| Anthropic API 실패·과부하 | 키워드 검색으로 폴백 |
| SQS 전송 실패 | gateway가 그 자리에서 답함 (느리지만 답은 감) |

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

## 네가 할 일 — Slack 앱 만들기 (봇 유저)

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

> **rate limit:** 공개 repo는 토큰 없이 읽지만, GitHub 무인증 API는 시간당 60회 제한.
> 툴 콜링은 한 질문에 여러 번 위키를 읽으므로 60초 캐시를 둬서 같은 스냅샷을 재사용한다.
> 그래도 한도에 걸리면 `.env`의 `GITHUB_TOKEN=`에 읽기 전용 fine-grained PAT를 채우고
> 재배포하면 5000/시간.

## 다음 단계

1. **쓰기(STEP 2)** — `file_note` / `update_note`로 정리·커밋. 모든 쓰기는 diff 미리보기 + 승인(PR 또는 👍) 경유, 무조건 덮어쓰기 금지.
2. **검색 품질** — 한국어 조사 처리, 별칭·태그 가중치 조정.
3. **MCP 분리(학습)** — `internal/wiki`를 별도 MCP 서버로 떼어 Claude 데스크톱에서도 사용.
