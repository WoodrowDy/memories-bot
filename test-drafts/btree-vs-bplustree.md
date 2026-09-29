---
title: B-tree vs B+tree
aliases:
  - b-tree
  - b+tree
  - 비플러스트리
created: 2026-07-30
updated:
tags:
  - cs/datastructure
  - cs/index-internals
status:
---

# B-tree vs B+tree

## 핵심

둘 다 한 노드에 키를 여러 개 담는 균형 트리다. 이진 트리와 갈리는 지점이 여기다 —
노드 하나가 디스크 블록 하나에 맞게 커서, 높이가 낮고 그래서 디스크 접근 횟수가 적다.

차이는 두 개다.

첫째, 값이 어디 있는가. B-tree는 내부 노드에도 값을 들고 있고, B+tree는 리프에만
있다. 내부 노드가 키만 들고 있으면 같은 블록에 키를 더 많이 담을 수 있고, 팬아웃이
커지니 높이가 더 낮아진다.

둘째, 리프가 이어져 있는가. B+tree는 리프끼리 연결 리스트로 이어져 있다. 범위 조회가
"시작점을 찾고 옆으로 걸어가면 끝"이 되는 이유다. B-tree로 범위 조회를 하면 트리를
위아래로 오가야 한다.

그래서 관계형 DB 인덱스는 대부분 B+tree다. `WHERE created_at BETWEEN ...`이나
`ORDER BY id LIMIT 20` 같은 것이 인덱스 스캔으로 끝나야 하니까.

> [!NOTE]
> 팬아웃이 커지면 높이가 낮아진다는 게 체감이 안 됐는데, 100만 행에 팬아웃이
> 200이면 높이가 3이다. 조회 하나에 블록 읽기 세 번.

로컬 PostgreSQL에 100만 행을 넣고 `EXPLAIN (ANALYZE, BUFFERS)`로 봤더니 인덱스
스캔은 `shared hit=4`였고, 같은 조건에 `enable_indexscan = off`로 seq scan을
강제하니 `hit=8000`을 넘겼다. 높이가 낮다는 말이 읽는 블록 수로 그대로 나왔다.

실행 계획 읽는 법은 [[인덱스]] 노트에 있다.
