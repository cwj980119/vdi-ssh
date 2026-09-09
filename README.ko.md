# VDI SSH 0.1.0

사내 Windows VDI에서 실행하는 설치형 서비스 없는 SSH 서버입니다. VDI에 `vdi-ssh.exe`를 복사하고 실행하면, 허용한 사내 PC에서 일반 `ssh` 클라이언트로 PowerShell을 사용할 수 있습니다. VDI에 OpenSSH, Python, .NET 또는 Go를 설치할 필요가 없습니다.

## 가능한 기능

- Ed25519 공개키 인증과 SSH 암호화 통신
- 대화형 PowerShell: 명령 입력, 방향키, 한글, 터미널 크기 변경
- `ssh ... "명령"`을 통한 명령 실행, 표준 입력, 표준 출력/오류, 종료 코드
- 접속 PC의 IP 또는 CIDR 허용 목록
- 접속과 세션 시작·종료 기록, 로그 회전
- 연결 종료 시 해당 세션의 프로세스 트리 종료

현재 버전은 화면 접속, SFTP/SCP, 포트 포워딩, SSH agent forwarding, VS Code Remote SSH를 제공하지 않습니다. SSH 접속으로 다른 Windows 계정에 로그인하는 기능도 없습니다. SSH 사용자 이름 `vdi`는 접속용 별칭이며, **모든 명령은 서버 EXE를 실행한 Windows 계정과 권한으로 실행됩니다.** EXE를 관리자 권한으로 실행하면 원격 명령도 그 권한을 갖습니다. 일반적인 명령 작업에는 일반 사용자로 실행하면 됩니다.

## 실행 조건

- VDI: Windows 10 1809 / Windows Server 2019 이상, x64. 대화형 터미널은 Windows ConPTY를 사용합니다.
- 접속 PC: `ssh`, `ssh-keygen` 클라이언트 사용 가능
- 접속 PC → VDI의 지정 TCP 포트로 연결 가능
- 회사에서 허용한 EXE 실행 및 원격접속 경로

서버 창은 실행 상태로 유지해야 합니다. 재부팅이나 Windows 로그오프 후에는 다시 실행해야 합니다. VDI 클라이언트 연결을 끊었을 때 Windows 세션이 유지되는지는 VDI 정책에 따릅니다. 서버는 서비스·자동 시작·방화벽 규칙을 자동 등록하지 않습니다.

## 1. 접속할 사내 PC에서 키 만들기

PowerShell에서 실행합니다. 같은 이름의 키가 이미 있다면 새 파일 이름을 사용하십시오.

```powershell
ssh-keygen -t ed25519 -f "$env:USERPROFILE\.ssh\vdi_access" -C "vdi-access"
```

키 생성 중 암호를 설정할 수 있습니다. **`vdi_access.pub` 공개키만 VDI로 복사**합니다. 개인키 `vdi_access`는 접속 PC에 보관합니다.

## 2. VDI에서 초기 설정

배포 ZIP을 사용자 폴더에 풀고 해당 폴더에서 PowerShell을 엽니다. 아래 IP 주소를 실제 주소로 바꿉니다.

- 예시 VDI 주소: `10.20.30.40`
- 예시 접속 PC 주소: `10.20.30.50`

```powershell
.\vdi-ssh.exe init --public-key .\vdi_access.pub --listen 10.20.30.40:2222 --allow-ip 10.20.30.50
```

초기 설정은 기본적으로 `%APPDATA%\VDISSH`에 다음 파일을 만듭니다.

| 파일 | 용도 |
|---|---|
| `config.json` | 수신 주소, 접속 별칭, 허용 IP, 시작 디렉터리 |
| `authorized_keys` | 접속을 허용할 공개키 |
| `host_ed25519` | 서버의 개인 호스트 키 — 공유하지 마십시오 |
| `audit.jsonl` | 실행 후 생성되는 접속 로그 |

이 디렉터리는 생성 시 현재 Windows 사용자와 SYSTEM만 접근하도록 ACL을 설정합니다. 초기 설정은 기존 디렉터리를 덮어쓰지 않습니다. IP 변경 등은 `config.json`을 편집하고 서버를 재시작하면 됩니다. 호스트 키는 계속 보관해야 서버의 식별 지문이 유지됩니다.

여러 PC를 허용하려면 `--allow-ip 10.20.30.50,10.20.30.51`을 사용할 수 있습니다. `10.20.30.0/24` 같은 CIDR도 지원합니다. 수신 주소는 VDI에 실제 할당된 특정 IP여야 합니다. IPv6는 `[주소]:2222` 형식으로 지정합니다.

설정 위치를 바꾸려면 `init`과 `serve`에 동일한 `--data-dir C:\Users\사용자\VDISSH`를 지정합니다.

## 3. VDI에서 서버 실행

```powershell
.\vdi-ssh.exe serve
```

화면에 Windows 실행 계정, 수신 주소, `SHA256:...` 호스트 키 지문이 표시됩니다. 이 창을 열어둡니다. `Ctrl+C`로 종료합니다.

## 4. 사내 PC에서 접속

```powershell
ssh -i "$env:USERPROFILE\.ssh\vdi_access" -p 2222 vdi@10.20.30.40
```

최초 접속 시 표시되는 호스트 키 지문이 **VDI 서버 창에 표시된 지문과 같은지** 확인하고 연결을 승인합니다. 다음 접속부터 클라이언트가 저장된 지문을 확인합니다. 접속 후 `whoami`, `Get-Location`, `Get-Process` 등을 실행할 수 있습니다. `exit`로 세션을 끝냅니다.

명령 하나만 실행할 수도 있습니다. 명령 문법은 Windows PowerShell입니다.

```powershell
ssh -i "$env:USERPROFILE\.ssh\vdi_access" -p 2222 vdi@10.20.30.40 "Get-Date; whoami"
```

키를 여러 개 사용하는 PC에서는 `-o IdentitiesOnly=yes`를 추가하면 지정한 키만 사용합니다. 연결당 동시 세션 1개, 서버 전체 동시 연결 4개가 기본값입니다. 독립적인 `ssh` 연결을 추가로 열 수 있습니다. 창 없는 shell 요청에는 PTY가 필요하므로 파이프에서 대화형 연결을 시도한다면 `ssh -tt ...`를 사용합니다.

## 접속이 안 될 때

VDI에서 실행 환경과 주소를 확인합니다.

```powershell
.\vdi-ssh.exe doctor
```

접속 PC에서 서버를 실행해 둔 VDI의 포트를 확인합니다.

```powershell
Test-NetConnection -ComputerName 10.20.30.40 -Port 2222
```

또는 같은 EXE를 접속 PC에 복사하고 다음 진단을 실행합니다.

```powershell
.\vdi-ssh.exe doctor --target 10.20.30.40:2222
```

- **TCP 실패:** 서버 실행 여부, 수신 IP, 경로, 방화벽의 해당 포트 허용 상태를 확인합니다. 포트에서 수신하는 프로그램이 없을 때도 실패하므로, 실패 결과만으로 방화벽 문제라고 단정할 수 없습니다.
- **TCP 성공 후 SSH 끊김:** `config.json`의 `allow_from`이 실제 접속 PC의 출발지 IP와 맞는지 확인합니다. NAT가 있다면 VDI에서 보이는 주소를 사용합니다.
- **Permission denied (publickey):** 접속 별칭이 기본값 `vdi`인지, 개인키와 VDI에 복사한 공개키가 한 쌍인지 확인합니다. 이 버전은 Ed25519 키만 허용합니다.
- **대화형 셸만 실패:** Windows 버전과 ConPTY 사용 가능 여부, 서버의 `session_start_failed` 로그를 확인합니다. VDI의 프로세스 Job 제한도 영향을 줄 수 있습니다.
- **PowerShell이 모듈 신뢰 여부를 묻는 경우:** 서버가 실행된 환경의 PowerShell 모듈 경로와 회사의 서명 정책을 확인합니다. 프로그램은 실행 정책을 변경하지 않습니다.
- **한글 오류 출력에 XML이 붙는 경우:** Windows PowerShell이 비대화형 오류를 CLIXML로 출력할 수 있습니다. 일반 출력은 UTF-8이며, 대화형 접속으로도 확인할 수 있습니다.

방화벽에 예외가 필요한 경우, 회사에서 허용한 범위에서 VDI의 TCP 2222를 접속 PC의 IP에만 허용하면 됩니다. 프로그램은 방화벽 설정을 변경하지 않습니다.

## 키 변경과 로그

`authorized_keys`에는 일반 OpenSSH `.pub` 형식의 Ed25519 공개키를 한 줄씩 넣습니다. `command=`, `from=`, `no-pty` 등 OpenSSH 키 옵션은 지원하지 않으며, 옵션이 있는 키 파일은 거부합니다. IP 제한은 `config.json`에서 설정합니다.

키 파일은 인증 요청 때마다 다시 읽습니다. 공개키를 삭제하면 새 접속을 막을 수 있습니다. 이미 연결된 세션까지 끊으려면 서버를 종료한 뒤 다시 실행합니다.

로그는 접속 IP, 인증된 공개키의 지문, 세션 종류, 프로세스 ID, 종료 코드를 기록합니다. 명령 내용과 출력은 기록하지 않습니다. `audit.jsonl`과 이전 파일 `.1`을 각각 최대 약 4 MiB로 유지합니다. 서버에서 30초마다 SSH keepalive를 보내 연결이 사라진 세션을 정리합니다. 네트워크 단절 감지에는 시간이 걸릴 수 있습니다.

## 빌드와 검증

개발 PC에 Go 1.26 이상이 있으면 다음을 실행합니다. 의존성은 `go.mod`와 `go.sum`으로 고정됩니다. 빌드 도구는 VDI에 필요하지 않습니다.

```powershell
.\scripts\build.ps1
```

빌드 스크립트는 Go 검사와 통합 테스트를 실행하고 `dist\VDI-SSH` 폴더, 라이선스, 배포 ZIP과 SHA-256 해시를 만듭니다. 공개키 인증·거부·삭제, IP 차단, PowerShell 한글/입력/종료 코드, 대화형 PTY/크기 변경, 연결 종료 시 하위 프로세스 정리, Windows OpenSSH 클라이언트 호환성을 테스트합니다.

현재 검증은 개발 PC의 loopback 연결에서 수행했습니다. 실제 사내 VDI의 네트워크, 보안 제품, 화면 연결 해제 후 세션 유지 동작은 대상 환경에서 확인해야 합니다. 배포 EXE는 코드 서명되지 않았습니다.

## 라이선스와 참고

이 프로젝트의 직접 의존성은 Go의 `golang.org/x/crypto`와 `golang.org/x/sys`이며 BSD 계열 라이선스를 사용합니다. 배포물에 Go 런타임과 의존성 라이선스 원문을 동봉합니다. 유료 원격접속 서비스 계정이나 외부 중계 서비스는 사용하지 않습니다. 회사 내 반입 절차에는 배포물의 라이선스와 소스를 제출할 수 있습니다.

- [Go SSH 패키지](https://pkg.go.dev/golang.org/x/crypto/ssh)
- [Windows Pseudoconsole 설명](https://learn.microsoft.com/en-us/windows/console/creating-a-pseudoconsole-session)
- [Test-NetConnection](https://learn.microsoft.com/en-us/powershell/module/nettcpip/test-netconnection)
