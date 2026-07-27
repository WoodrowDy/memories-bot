# memories-wiki-bot

Slack에서 `@봇 동시성 있어?` 하면 내 GitHub 위키(`memories`)를 뒤져 답하는 봇.
**MVP: 읽기 전용** (검색 + 현황). A0 게이트웨이 골격 재사용.

## 지금 되는 것

```
Slack에서 봇 멘션(@위키봇 동시성 있어?)
  → API Gateway → Go Lambda
  → Slack 서명 검증
  → 멘션 텍스트 = 검색어
  → memories를 GitHub API로 읽어 검색 (공개 repo, 토큰 0)
  → 봇 이름으로 스레드에 답 (있음/없음 · 경로 · status · 요약 · 링크)
```

- `@봇 동시성 있어?` → ✅ 경로·status·요약·GitHub 링크
- `@봇 카프카 있어?` → ❌ 없음
- `@봇 현황` / `@봇 위키 현황` → 카테고리·성숙도·daily/personal 요약

MVP에서 **안 하는 것**: LLM(자연어 추론), 쓰기/커밋, 큐. (다음 단계)

## 구조

```
cmd/bot/main.go          # Slack 이벤트 → 검색 → 답장 오케스트레이션
internal/wiki/           # search_wiki · wiki_status (GitHub API로 memories 읽기)
internal/render/         # 결과 → Slack 메시지
internal/slackevents/    # 서명 검증 · 이벤트 파싱   (A0 재사용)
internal/slackclient/    # chat.postMessage (봇으로 답)  (A0 재사용)
internal/audit/          # 감사 로그                 (A0 재사용)
internal/config/         # 대상 repo(owner/name/branch)
serverless.yml           # provided.al2023 / arm64 / POST /slack/events
```

## Slack 앱 만들기 (봇 유저)

> 지금 Cowork에 연결한 'Slack 커넥터'와 **별개**. 이건 채널에 상주할 *네 봇*을 만드는 것.

1. https://api.slack.com/apps → **Create New App** → From scratch. (개인 테스트 워크스페이스 권장)
2. **OAuth & Permissions → Bot Token Scopes**: `app_mentions:read`, `chat:write` 추가 → **Install to Workspace** → `xoxb-...` **Bot User OAuth Token** 확보.
3. **Basic Information → Signing Secret** 확보.
4. **Event Subscriptions → Enable** → Request URL에 배포된 `https://.../slack/events` 입력(서명시크릿 설정 후 검증 통과). **Subscribe to bot events → `app_mention`** 추가 → 저장.
5. 채널에 봇 초대: 대상 채널에서 `/invite @위키봇`.

## 빌드 · 배포

```bash
# 로컬 검증
go vet ./... && go build ./... && go test ./...
s
# 시크릿은 .env 파일로 관리한다 (.gitignore에 걸려 커밋되지 않음)
cp .env.example .env      # 처음 한 번만 — SLACK_SIGNING_SECRET / SLACK_BOT_TOKEN을 채운다

# 배포 (Serverless Framework: npm i -g serverless@3, AWS 자격증명 필요)
./deploy.sh               # .env 로드 → vet·test 게이트 → arm64 빌드 → deploy
```

배포 후 나온 HTTP API URL + `/slack/events`를 4번 Request URL에 등록.
다른 위키를 쓰려면 `WIKI_OWNER` / `WIKI_REPO` / `WIKI_BRANCH` 환경변수로 교체.

> **rate limit:** 공개 repo는 토큰 없이 읽지만, GitHub 무인증 API는 시간당 60회 제한.
> 질의당 트리 API 1회 + raw 파일 N회(raw는 제한이 느슨). 개인용엔 충분하지만,
> 한도에 걸리면 `.env`의 `GITHUB_TOKEN=`에 읽기 전용 fine-grained PAT를 채우고 재배포하면 5000/시간.

## 다음 단계

1. **자연어 브레인** — 멘션을 LLM(툴콜링)에 넘겨 "요즘 뭐 배웠지?" 같은 자유 질의 처리.
2. **쓰기(STEP 2)** — `file_note` / `update_note`로 정리·커밋(토큰 or 로컬 브리지).
3. **MCP 분리(학습)** — `internal/wiki`를 별도 MCP 서버로 떼어 Claude 데스크톱에서도 사용.