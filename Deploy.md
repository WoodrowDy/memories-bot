# 배포 가이드 (AWS Lambda)

봇은 **AWS에서 24/7 실행**됨. 로컬 도구는 '빌드 + 업로드'용으로 한 번만 씀 (배포 후 노트북 꺼도 봇은 동작).

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
2. **OAuth & Permissions → Bot Token Scopes**: `app_mentions:read`, `chat:write`, `files:read`
   - `files:read`는 `.md` 첨부를 받기 위한 것. 없으면 슬랙이 이벤트에서 `files[]`를
     **통째로 빼고** 보내서, 봇은 파일이 왔다는 사실조차 모른다 (에러도 안 난다).
     붙여넣기로 쓸 거면 없어도 되지만, 그때는 첨부가 조용히 무시된다는 걸 알고 써야 한다.
3. 위쪽 **Install to Workspace** → 승인 → **Bot User OAuth Token**(`xoxb-...`) 복사
4. **Basic Information → Signing Secret** 복사

## ②-b Anthropic API 키 — 브라우저 (자연어 브레인용, 선택)

https://console.anthropic.com/settings/keys → **Create Key** → `sk-ant-...` 복사.
없어도 배포는 되고 봇도 답한다 — 다만 키워드 검색 수준으로 내려간다.

## ③ 배포 — 로컬 터미널

```bash
cp .env.example .env            # 처음 한 번만
```

`.env`를 열어 채운다 — `SLACK_SIGNING_SECRET`, `SLACK_BOT_TOKEN`(필수),
`ANTHROPIC_API_KEY`(자연어 브레인), `GITHUB_TOKEN`(선택).

```bash
./deploy.sh                     # .env 로드 → vet·test → arm64 빌드 → serverless deploy
```

`.env`는 `.gitignore`에 걸려 있어 커밋되지 않는다. 한 번 채워두면 다음부터는 `./deploy.sh`만 치면 된다.

출력의 `endpoint:` URL 복사 (예: `https://xxxx.execute-api.ap-northeast-2.amazonaws.com`).
봇 주소 = 그 URL + `/slack/events`.

> ⚠️ 2단계에서 람다 이름이 `bot` → `gateway`로 바뀌었다. HTTP API는 그대로 재사용되므로
> 보통 endpoint URL은 안 변하지만, **출력의 `endpoint:` 줄과 슬랙에 등록된 Request URL이
> 같은지 눈으로 한 번 대조할 것.** 다르면 새 URL로 갱신하고 Verified를 다시 받는다.
> 옛 `bot` 함수와 그 로그 그룹은 배포 때 CloudFormation이 알아서 지운다.

## ④ 슬랙 Event 연결 — 브라우저 (이제 URL이 생겼으니)

1. 슬랙 앱 → **Event Subscriptions → Enable**
2. **Request URL** = `https://xxxx.../slack/events` → 초록 **Verified** 확인
3. **Subscribe to bot events** → `app_mention` 추가 → **Save**
4. 재설치 뜨면 **Reinstall** — ⚠️ 재설치하면 `xoxb-` 토큰이 **새로 발급되어 옛 토큰이 죽는다.** OAuth & Permissions에서 새 값을 복사해 `.env`를 갱신하고 `./deploy.sh` 다시.
5. **Socket Mode는 반드시 Off** (Settings → Socket Mode). 켜져 있으면 슬랙이 Request URL로 이벤트를 보내지 않는다.

## ⑤ 테스트 — 슬랙

```
/invite @<봇이름>
@<봇이름> 동시성 정리한 거 있어?
@<봇이름> 요즘 뭐 배웠지?
```

봇이 스레드에 답하면 완료 🎉
두 번째 질문에 목록을 훑고 요약해서 답하면 자연어 브레인이 살아있는 것이다.

## 잘 붙었는지 확인 (CloudWatch)

`worker` 로그에 이 두 줄이 보이면 정상:

```
boot: brain=true queue=true model=claude-haiku-4-5
brain: turns=2 in=4210 out=180 tools=[search_wiki read_note]
```

- `brain=false` → `ANTHROPIC_API_KEY`가 안 들어갔다 (키워드 모드)
- `queue=false` → `JOBS_QUEUE_URL`이 비었다. serverless.yml의 `Ref: JobsQueue` 확인

## 막히면

- **Verified 실패** → `SLACK_SIGNING_SECRET`이 배포 env와 같은지 / URL 끝 `/slack/events` 맞는지
- **URL은 Verified인데 Lambda 로그가 텅 빔** → **Socket Mode가 켜져 있다.** Settings → Socket Mode → Off. (슬랙이 HTTP를 안 쏘고 WebSocket을 기다리는 상태)
- **봇 조용** → 채널에 초대했는지 / `app_mention` 구독+재설치 했는지 / CloudWatch 로그 확인
- **답이 늦거나 안 옴 (gateway 로그엔 있는데 worker가 조용)** → SQS 이벤트 소스가 안 붙었다. Lambda 콘솔 → worker → Triggers에 SQS가 있는지 확인
- **같은 답이 두 번** → `worker`가 에러를 던져 SQS가 재전송한 것. DLQ(`...-jobs-dlq`)를 보면 원인이 남아 있다
- **`slack post: invalid_auth`** → 재설치하며 `xoxb-`가 로테이션됨. 새 토큰을 `.env`에 넣고 재배포
- **`.md`를 첨부했는데 봇이 못 본 척한다** → `files:read` 스코프가 없다. 슬랙이 이벤트에서
  `files[]`를 빼고 보내므로 봇 쪽엔 아무 흔적도 안 남는다. 스코프 추가 → **Reinstall** →
  새 `xoxb-`를 `.env`에 → 재배포
- **봇이 `봇 토큰에 files:read 스코프가 없어 보여요`라고 답한다** → 스코프는 넣었는데 재설치를
  안 했거나, 재설치로 새로 나온 `xoxb-`를 `.env`에 안 넣었다. 파일이 온 건 봤으니 이벤트
  구독은 멀쩡하고 다운로드만 막힌 상태다. Reinstall → 새 토큰 → 재배포
- **답이 항상 키워드 검색 수준** → CloudWatch에서 `brain: ... falling back` 줄을 찾아 이유 확인 (키 오타 / 크레딧 소진 / 모델명 오타)
- **배포 권한 에러** → IAM에 Lambda·APIGateway·CloudFormation·IAM·SQS 권한
- **`UnreservedConcurrentExecutions below its minimum value of [10]`** → 계정 동시 실행 한도가 낮은
  새 계정이다. 이미 `serverless.yml`에서 `reservedConcurrency` 대신 SQS 이벤트의
  `maximumConcurrency`를 쓰도록 고쳐놨으니 지금 코드에선 안 난다. 혹시 또 나오면 그건
  어딘가에 `reservedConcurrency`가 다시 들어간 것 — 지우면 된다
- **GitHub 403(rate limit)** → `.env`의 `GITHUB_TOKEN=`에 읽기 전용 PAT를 채우고 재배포. 공개 repo면 fine-grained PAT의 "Public Repositories (read-only)"로 충분
- **싹 삭제** → `npx serverless remove`
