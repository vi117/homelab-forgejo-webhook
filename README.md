# homelab-forgejo-webhook

Forgejo의 push webhook을 검증하고 공개 정적 저장소를
[git-pages](https://git-pages.org/)에 배포하는 작은 어댑터입니다.

다음 검증을 통과한 요청만 배포합니다.

- `X-Forgejo-Signature` HMAC-SHA256 서명
- 설정된 Forgejo 소유자와 저장소 이름
- Forgejo API가 반환한 공개 여부, 기본 브랜치, clone URL
- 40자 또는 64자 소문자 16진수 commit ID
- 기본 브랜치에 대한 push 이벤트

저장소별 배포는 직렬화되며, 같은 commit을 다시 받은 경우 중복 배포하지 않습니다.

## 코드 구조

| 경로 | 책임 |
| --- | --- |
| `cmd/homelab-forgejo-webhook` | 설정을 읽고 HTTP 서버와 클라이언트를 조립 |
| `internal/config` | 환경 변수 파싱과 검증 |
| `internal/forgejo` | Forgejo API 저장소 조회와 준비 상태 검사 |
| `internal/gitpages` | git-pages 갱신 요청과 manifest 확인 |
| `internal/webhook` | HMAC 검증, 정책 적용, 배포 조정, 메트릭 |

Forgejo 조회 코드와 git-pages 갱신 코드는 HTTP webhook 처리에서 분리되어 있습니다.

## 설정

배포 환경을 특정하는 값에는 기본값이 없습니다. 다음 환경 변수를 모두 직접 지정해야
합니다.

| 이름 | 설명 |
| --- | --- |
| `FORGEJO_API_URL` | 저장소 검증에 사용할 Forgejo API 기준 URL |
| `FORGEJO_PUBLIC_URL` | 공개 clone URL을 만들 Forgejo 기준 URL |
| `FORGEJO_PAGES_OWNER` | 배포를 허용할 Forgejo 사용자 또는 조직 |
| `PAGES_DOMAIN` | 저장소 이름을 앞에 붙일 Pages 기준 도메인 |
| `GIT_PAGES_URL` | 내부 git-pages 갱신 endpoint |
| `WEBHOOK_SECRET` | Forgejo와 공유할 32바이트 이상의 HMAC secret |

다음 운영 설정만 안전한 기본값을 가집니다.

| 이름 | 기본값 | 설명 |
| --- | --- | --- |
| `LISTEN_ADDRESS` | `:8081` | HTTP 수신 주소 |
| `MAX_WEBHOOK_BYTES` | `1048576` | webhook 본문 최대 크기 |
| `DEPLOY_TIMEOUT` | `90s` | 갱신과 manifest 확인 제한 시간 |

[`.env.example`](.env.example)은 문서용 일반 예시이며 프로그램이 `.env` 파일을
자동으로 읽지는 않습니다. 실제 `.env` 파일은 Git에서 제외됩니다.

## 실행

환경 변수를 설정한 뒤 실행합니다.

```console
go run ./cmd/homelab-forgejo-webhook
```

`FORGEJO_PAGES_OWNER`로 지정한 조직에는 조직 webhook 하나만 등록하면 됩니다.
Target URL은 다음과 같습니다.

```text
http://<서비스 주소>:8081/hooks/forgejo
```

Content Type은 `application/json`, 이벤트는 Push Events를 선택하고
`WEBHOOK_SECRET`과 같은 Secret을 입력합니다.
서명 검증이 끝난 뒤 payload의 저장소 이름을 사용하며, Forgejo API로 소유자와
공개 여부를 다시 확인합니다. 기존 저장소별 URL도 호환을 위해 계속 지원합니다.

## HTTP endpoint

| 경로 | 용도 |
| --- | --- |
| `/hooks/forgejo` | 조직 전체에 사용하는 Forgejo push webhook |
| `/hooks/forgejo/<저장소>` | 기존 저장소별 Forgejo push webhook |
| `/healthz` | 프로세스 상태 |
| `/readyz` | Forgejo와 git-pages 의존성 상태 |
| `/metrics` | Prometheus 형식 메트릭 |

## 검사와 이미지

```console
go test -race ./...
go vet ./...
docker build .
```

`main` 브랜치에 push하면 GitHub Actions가 다음 경로에 이미지를 발행합니다.

```text
ghcr.io/<GitHub 사용자 또는 조직>/homelab-forgejo-webhook:<commit-sha>
```

공개 클러스터에서 image pull Secret 없이 사용하려면 발행된 GHCR package도 공개로
설정해야 합니다. 운영 매니페스트에서는 `main` 태그보다 검증한 digest 고정을
권장합니다.
