---
title: 레이어드 아키텍처
aliases:
  - layered architecture
  - 계층형 아키텍처
created: 2026-07-30
updated:
tags:
  - cs/architecture
  - cs/layering
status:
---

# 레이어드 아키텍처(Layered Architecture)

> 백엔드(NestJS / Spring) 관점에서 계층을 왜 나누는지, 어디서 무너지는지 정리

## 1. 한 줄 정의

관심사를 층으로 나누고 **의존 방향을 한쪽으로만** 흐르게 하는 구조.
위층은 아래층을 알지만, 아래층은 위층을 몰라야 한다.

## 2. 층 나누기

| 층 | 하는 일 | 알아도 되는 것 |
|------|--------|----------|
| Presentation (Controller) | HTTP 요청/응답 변환 | Application |
| Application (Service) | 유스케이스 조립, 트랜잭션 경계 | Domain, Infra 인터페이스 |
| Domain | 규칙과 불변식 | 아무것도 (순수) |
| Infrastructure | DB·큐·외부 API | Domain 인터페이스 구현 |

## 3. 왜 이 방향인가

Domain이 Infra를 모르면 DB를 바꿔도 규칙은 그대로 산다. 반대로 엔티티에
`@Column`이 박히는 순간 그 규칙은 그 DB의 것이 된다.

```ts
// Service가 Repository 인터페이스에만 의존한다 (구현은 Infra가 준다)
class TransferService {
  constructor(private readonly accounts: AccountRepository) {}
}
```

## 4. 실제로 무너지는 자리

- Controller가 Repository를 직접 부른다 → Application 층이 장식이 된다
- 트랜잭션 경계가 Controller에 있다 → 유스케이스 하나가 두 층에 걸친다
- Domain 객체에 ORM 데코레이터가 붙는다 → 순수성이 깨진다
- DTO를 Domain까지 들고 내려간다 → 표현 형식이 규칙을 오염시킨다

## 5. 면접용 핵심 요약

- 층을 나누는 목적은 파일 정리가 아니라 **의존 방향 고정**이다
- 방향이 지켜지면 아래층은 위층 없이도 테스트된다
- 레이어드의 한계는 층을 관통하는 변경 — 그래서 DDD/헥사고날 얘기가 나온다
