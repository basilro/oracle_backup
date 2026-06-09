# 매뉴얼 탭(사용 가이드) 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 대시보드에 "매뉴얼" 탭을 추가해 전체 기능 사용 가이드를 정적 HTML로 제공한다.

**Architecture:** `nav.tabs`에 탭 버튼, 마지막에 `tabpanel[data-panel=manual]` 추가. 기존 `showTab`이 임의 패널을 처리하므로 JS 로직 변경 없음(BUILD 스탬프만 갱신). CSS는 가독성 규칙 소폭 추가.

**Tech Stack:** 정적 HTML/CSS, vanilla JS(기존).

**설계 문서:** `docs/specs/2026-06-09-manual-tab-design.md`

---

## 파일 구조

| 파일 | 변경 |
|------|------|
| `engine/web/ui/index.html` | 탭 버튼 + 매뉴얼 패널(카드 7개) + 캐시버스트 `?v=` |
| `engine/web/ui/style.css` | 매뉴얼 가독성 규칙(`.manual` 등) |
| `engine/web/ui/app.js` | BUILD 스탬프만 갱신 |

---

## Task 1: 탭 버튼 + 매뉴얼 패널

**Files:**
- Modify: `engine/web/ui/index.html`

- [ ] **Step 1: nav.tabs에 매뉴얼 탭 버튼 추가**

`engine/web/ui/index.html`의:
```html
      <button class="tab" data-tab="restore">복원</button>
    </nav>
```
을 아래로 교체:
```html
      <button class="tab" data-tab="restore">복원</button>
      <button class="tab" data-tab="manual">매뉴얼</button>
    </nav>
```

- [ ] **Step 2: 복원 패널 다음에 매뉴얼 패널 추가**

`engine/web/ui/index.html`에서 복원 패널이 닫히는 부분을 찾는다. 복원 패널은 이렇게 끝난다:
```html
        <div class="row-actions">
          <button id="restoreBtn" class="btn-primary">복원 실행</button>
          <button id="rdl" class="btn-ghost" style="display:none">결과 다운로드 (.tar.gz)</button>
          <span id="rmsg" class="msg"></span>
        </div>
      </div>
    </section>
```
이 `</section>`(복원 패널 닫음) 다음에 아래 매뉴얼 패널을 삽입한다:
```html

    <!-- 매뉴얼 -->
    <section class="tabpanel" data-panel="manual" hidden>
      <div class="card manual">
        <h2>1. 시작하기 (최초 설정)</h2>
        <p class="dim">서버에 처음 올릴 때 필요한 것들입니다. 자세한 절차는 저장소 README를 참고하세요.</p>
        <ul>
          <li><code>.env</code> — <code>REMOTE_NAME</code>(rclone 원격 이름), <code>HOST_TAG</code>(이 서버 식별자) 설정. <b>서버마다 HOST_TAG는 다르게</b> 주세요(아래 6번 참고).</li>
          <li><code>rclone/rclone.conf</code> — 백업을 보낼 원격(Google Drive 등). "목적지" 탭에서 추가/편집할 수 있습니다.</li>
          <li><code>secrets/repo-pass</code> — restic 저장소 암호. <b>이 파일만으로 관리</b>하며 UI엔 절대 노출되지 않습니다.</li>
          <li>최초 저장소 생성은 <code>ALLOW_REPO_INIT=true</code>로 한 번 init 후 되돌리는 것을 권장합니다.</li>
        </ul>
        <div class="manual-warn">⚠️ <b>repo-pass를 잃어버리면 백업을 영구히 복구할 수 없습니다.</b> 안전한 곳에 따로 보관하세요.</div>
      </div>

      <div class="card manual">
        <h2>2. 개요 탭</h2>
        <ul>
          <li><b>지금 백업</b> — 스케줄과 무관하게 수동으로 즉시 백업을 실행합니다.</li>
          <li><b>스냅샷</b> — 원격 저장소의 백업 시점 목록(restic 스냅샷). 처음 조회는 원격 왕복으로 수 초~십수 초 걸릴 수 있습니다.</li>
          <li><b>실행 이력</b> — 최근 백업/검증 실행 결과·로그.</li>
        </ul>
      </div>

      <div class="card manual">
        <h2>3. 설정 탭</h2>
        <ul>
          <li><b>보존 일수</b>(KEEP_DAILY) — 최근 N일치 스냅샷 유지(나머지는 forget+prune).</li>
          <li><b>업로드 제한</b> — KB/s, 0=무제한.</li>
          <li><b>백업/무결성 스케줄</b> — cron(분 시 일 월 요일, KST). 자동 스케줄러 토글로 on/off.</li>
          <li><b>DB 일관성 백업</b> — 켜면 백업 전에 DB를 덤프합니다.</li>
          <li><b>제외 규칙</b> — restic 제외 패턴(한 줄에 하나). 다음 백업부터 적용.</li>
          <li><b>백업 대상 경로 · DB</b> — 소스 경로(기본 <code>/home</code>, <code>/var/lib/docker/volumes</code>는 항상 포함)와 DB 덤프 작업 목록(추가·삭제·on/off).</li>
          <li><b>알림 (webhook)</b> — Discord/Slack 호환 URL. "테스트 전송"으로 즉시 확인. 비우고 저장하면 알림 끔.</li>
        </ul>
      </div>

      <div class="card manual">
        <h2>4. 목적지 탭 (백업을 어디로 보낼지)</h2>
        <ul>
          <li><b>목적지 추가(간편)</b> — WebDAV·SFTP·FTP·S3를 한글 폼으로 추가.</li>
          <li><b>rclone 공식 설정</b> — Google Drive 등 OAuth는 "rclone 설정 열기"(GUI) 또는 "대화형 터미널"에서. CLI로 한 줄 명령도 가능.</li>
          <li><b>백업 저장 위치 — 경로 변경</b> — 현재 원격 안에서 저장소 경로를 바꿉니다. 기존 데이터를 새 경로로 <b>이동</b>(복사→검증→전환→원본 삭제)합니다.</li>
          <li><b>백업 저장 위치 — 원격 전환</b> — 다른 원격으로 전환(경로는 유지). 대상이 비어 있으면 <b>이동</b>, 대상에 이미 저장소가 있으면 <b>채택</b>(이동 없이 그 저장소로 전환, 원본 유지). 시작 전 어느 쪽인지 표시됩니다.</li>
        </ul>
      </div>

      <div class="card manual">
        <h2>5. 복원 탭</h2>
        <ul>
          <li>스냅샷을 고르고, "경로 선택…"으로 복원할 폴더를 모달에서 탐색·선택합니다(비우면 전체).</li>
          <li>관리자 비밀번호 재확인 후 실행. 결과는 <code>/restore-out</code>(안전한 임시 영역)으로 복원되며 <b>라이브 데이터는 절대 덮어쓰지 않습니다.</b></li>
          <li>"결과 다운로드(.tar.gz)"로 복원물을 내려받을 수 있습니다.</li>
        </ul>
      </div>

      <div class="card manual">
        <h2>6. 멀티 서버 운영</h2>
        <p class="dim">여러 서버에서 같은 원격으로 백업할 때.</p>
        <ul>
          <li><b>서버별 자동 분리</b> — 저장소 경로 기본값은 <code>원격:backups/&lt;HOST_TAG&gt;</code>. 서버마다 HOST_TAG만 다르게 주면 <b>완전히 별개의 저장소</b>가 됩니다(권장).</li>
          <li><b>같은 경로 공유</b> — 일부러 같은 경로를 쓰면 한 저장소를 공유합니다. 스냅샷은 host 태그로 구분되고 전역 중복제거가 되지만, 모든 서버가 같은 repo-pass를 써야 하고 <code>prune</code> 배타 잠금 때문에 스케줄을 겹치지 않게 해야 합니다.</li>
          <li>DB 없는 서버는 "DB 작업"을 빈 목록으로 저장하면 DB 단계를 건너뜁니다.</li>
        </ul>
      </div>

      <div class="card manual">
        <h2>7. 안전 · 주의사항</h2>
        <ul>
          <li><b>repo-pass 분실 = 복구 불가.</b> 별도 백업 필수.</li>
          <li><b>마이그레이션 안전</b> — 경로/원격 전환은 복사·검증을 통과하기 전에는 절대 활성 경로를 바꾸거나 원본을 삭제하지 않습니다. 중간 실패 시 원본이 그대로 활성 상태로 남습니다.</li>
          <li><b>동시 실행 차단</b> — 백업·복원·마이그레이션은 하나만 실행되도록 게이트로 직렬화됩니다.</li>
          <li><b>자격증명 비노출</b> — repo-pass·DB 비밀번호·rclone 토큰은 파일(secrets/rclone.conf)에서만 관리하며 UI에 표시되지 않습니다(알림 webhook·관리자 비번 제외).</li>
        </ul>
      </div>
    </section>
```

- [ ] **Step 3: 임시 검증 — 마크업 균형 확인**

Run:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && \
  echo "manual panel: $(grep -c 'data-panel=\"manual\"' index.html)" && \
  echo "manual tab: $(grep -c 'data-tab=\"manual\"' index.html)" && \
  echo "section open: $(grep -c '<section' index.html) / close: $(grep -c '</section>' index.html)"
```
Expected: manual panel=1, manual tab=1, section open == close

- [ ] **Step 4: 커밋**

```bash
cd /home/ubuntu/backup-stack && git add engine/web/ui/index.html
git commit -m "feat(ui): 매뉴얼 탭 + 전체 기능 가이드(정적 HTML)"
```

---

## Task 2: 스타일 + 캐시버스트 + 검증

**Files:**
- Modify: `engine/web/ui/style.css`
- Modify: `engine/web/ui/app.js`, `engine/web/ui/index.html`(캐시버스트)

- [ ] **Step 1: style.css — 매뉴얼 가독성 규칙 추가**

`engine/web/ui/style.css` 끝에 추가:
```css
.manual ul { margin:6px 0 0; padding-left:20px; }
.manual li { margin:5px 0; line-height:1.55; font-size:.9rem; }
.manual h2 { margin-bottom:6px; }
.manual-warn { margin-top:12px; padding:10px 12px; border-radius:8px; background:rgba(248,81,73,.12); border:1px solid rgba(248,81,73,.4); font-size:.86rem; }
```

- [ ] **Step 2: 캐시버스트 스탬프 갱신**

`engine/web/ui/app.js`의 1번 줄을:
```javascript
const BUILD = "ui-2026-06-09b";
```
로 바꾸고:
```bash
cd /home/ubuntu/backup-stack/engine/web/ui && sed -i 's/v=20260609a/v=20260609b/g' index.html && echo "count=$(grep -c 20260609b index.html)"
```
Expected: count=5

- [ ] **Step 3: JS 문법 검사(변경 최소지만 확인)**

Run: `cd /home/ubuntu/backup-stack/engine/web/ui && node --check app.js && echo OK`
Expected: OK

- [ ] **Step 4: 이미지 재빌드 + 기동 + 매뉴얼 배포 확인**

Run:
```bash
cd /home/ubuntu/backup-stack && docker compose up -d --build && sleep 6 && \
  curl -fsS http://localhost:8088/healthz && echo " healthz" && \
  echo "BUILD: $(curl -s 'http://localhost:8088/app.js?v=20260609b' | head -1)" && \
  echo "매뉴얼 마크업: $(curl -s 'http://localhost:8088/' >/dev/null; curl -s http://localhost:8088/login >/dev/null; echo via-embed)"
```
Expected: `ok healthz`, `BUILD: const BUILD = "ui-2026-06-09b";`

> 참고: `/`는 미인증 시 로그인으로 리다이렉트되므로 매뉴얼 HTML은 로그인 후 브라우저에서 확인한다.

- [ ] **Step 5: 수동 검증 (브라우저)**

1. 상단 탭에 "매뉴얼" 추가됨. 클릭 → 카드 7개 가이드 표시.
2. 다른 탭 ↔ 매뉴얼 전환 정상. 새로고침 후 `#manual` 유지.
3. 경고 박스(repo-pass) 빨간 톤으로 강조됨.

- [ ] **Step 6: 커밋**

```bash
cd /home/ubuntu/backup-stack && git add engine/web/ui/style.css engine/web/ui/app.js engine/web/ui/index.html
git commit -m "style(ui): 매뉴얼 가독성 규칙 + 캐시버스트 ui-2026-06-09b"
```

---

## 검증 체크리스트 (spec 대비)

- [x] 매뉴얼 탭 + 패널 추가 — index.html
- [x] JS 무변경(showTab 재사용) — BUILD 스탬프만 갱신
- [x] 전체 기능 가이드(카드 7개) — Task 1 Step 2
- [x] 가독성 스타일 — .manual / .manual-warn
- [x] 캐시버스트 — ui-2026-06-09b / v=20260609b
