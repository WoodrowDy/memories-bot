# 배포 가이드 (AWS Lambda)

봇은 **AWS에서 24/7 실행**됨. 로컬 도구는 '빌드 + 업로드'용으로 사용

## ① 준비 (한 번만) — 로컬 터미널

```bash
# Go, Node 설치돼 있어야 함 (go version / node -v)
npm i -g serverless@3            # v4는 계정 로그인 요구 → v3 권장
aws configure                   # AWS Access Key / Secret 입력
unzip memories-wiki-bot.zip && cd memories-wiki-bot
go vet ./... && go test ./...   # (선택) 로컬 검증
```

## ② 슬랙 앱 + 토큰 2개 — 브라우저 (배포에 필요하니 먼저)

1. https://api.slack.com/apps → **Create New App → From scratch** → 워크스페이스 선택
2. **OAuth & Permissions → Bot Token Scopes**: `app_mentions:read`, `chat:write`
3. 위쪽 **Install to Workspace** → 승인 → **Bot User OAuth Token**(`xoxb-...`) 복사
4. **Basic Information → Signing Secret** 복사

## ③ 배포 — 로컬 터미널

```bash
cp .env.example .env            # 처음 한 번만
# .env를 열어 SLACK_SIGNING_SECRET / SLACK_BOT_TOKEN 두 줄을 채운다
./deploy.sh                     # .env 로드 → vet·test → arm64 빌드 → serverless deploy
```

`.env`는 `.gitignore`에 걸려 있어 커밋되지 않는다. 한 번 채워두면 다음부터는 `./deploy.sh`만 치면 된다.

출력의 `endpoint:` URL 복사 (예: `https://xxxx.execute-api.ap-northeast-2.amazonaws.com`).
봇 주소 = 그 URL + `/slack/events`.

## ④ 슬랙 Event 연결 — 브라우저 (이제 URL이 생겼으니)

1. 슬랙 앱 → **Event Subscriptions → Enable**
2. **Request URL** = `https://xxxx.../slack/events` → 초록 **Verified** 확인
3. **Subscribe to bot events** → `app_mention` 추가 → **Save**
4. 재설치 뜨면 **Reinstall** — ⚠️ 재설치하면 `xoxb-` 토큰이 **새로 발급되어 옛 토큰이 죽는다.** OAuth & Permissions에서 새 값을 복사해 `.env`를 갱신하고 `./deploy.sh` 다시.
5. **Socket Mode는 반드시 Off** (Settings → Socket Mode). 켜져 있으면 슬랙이 Request URL로 이벤트를 보내지 않는다.

## ⑤ 테스트 — 슬랙

```
/invite @<봇이름>
@<봇이름> 동시성 있어?
```

봇이 스레드에 답하면 완료 🎉

## 막히면

- **Verified 실패** → `SLACK_SIGNING_SECRET`이 배포 env와 같은지 / URL 끝 `/slack/events` 맞는지
- **URL은 Verified인데 Lambda 로그가 텅 빔** → **Socket Mode가 켜져 있다.** Settings → Socket Mode → Off. (슬랙이 HTTP를 안 쏘고 WebSocket을 기다리는 상태)
- **봇 조용** → 채널에 초대했는지 / `app_mention` 구독+재설치 했는지 / CloudWatch 로그 확인
- **`slack post: invalid_auth`** → 재설치하며 `xoxb-`가 로테이션됨. 새 토큰을 `.env`에 넣고 재배포
- **배포 권한 에러** → IAM에 Lambda·APIGateway·CloudFormation·IAM 권한
- **GitHub 403(rate limit)** → `.env`의 `GITHUB_TOKEN=`에 읽기 전용 PAT를 채우고 재배포. 공개 repo면 fine-grained PAT의 "Public Repositories (read-only)"로 충분
- **싹 삭제** → `npx serverless remove`