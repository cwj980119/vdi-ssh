# VDI SSH

**Windows VDI에 EXE 하나를 실행하고, 다른 PC에서 SSH로 PowerShell에 접속하세요.**

설치 없이 실행하는 Windows용 SSH 서버입니다. VDI에 OpenSSH 서버, Python, .NET 또는 Go를 설치할 필요가 없습니다. 사내 PC와 VDI가 직접 통신하는 구조이며, 외부 중계 서비스나 서비스 계정도 필요하지 않습니다.

[실행 파일 ZIP 다운로드](https://github.com/cwj980119/vdi-ssh/raw/refs/heads/main/distribution/VDI-SSH-windows-x64.zip) · [상세 사용 설명서](README.ko.md) · [배포 파일 SHA-256](distribution/SHA256SUMS.txt)

## 지원 기능

| 기능 | 지원 |
|---|---|
| 대화형 PowerShell | 명령 입력, 방향키, 한글, 터미널 크기 변경 |
| 개별 명령 실행 | 표준 입력·출력·오류, 종료 코드 |
| SSH 인증 | Ed25519 공개키 |
| 접속 제한 | 특정 PC의 IP 또는 CIDR 허용 목록 |
| 파일 전송 | SFTP, 현재 OpenSSH `scp` 기본 모드 |
| 화면 공유와 제어 | HTTPS 브라우저 화면, 마우스·키보드 입력 |
| VS Code Remote SSH | SFTP와 VDI 내부 loopback 포트 전달 |
| 세션 관리 | 접속 로그, 연결 종료 시 세션의 프로세스 트리 정리 |
| 일반 포트 포워딩 | 미지원 — VS Code용 VDI loopback 연결만 허용 |

## 실행 환경

- **VDI:** Windows 10 1809 / Windows Server 2019 이상, x64
- **접속 PC:** `ssh`와 `ssh-keygen` 클라이언트 사용 가능
- **네트워크:** 접속 PC에서 VDI의 지정 TCP 포트로 직접 연결 가능
- **권한:** 회사에서 허용한 EXE 실행과 원격접속 경로

대화형 터미널에는 Windows ConPTY를 사용합니다. 원격 명령은 **서버 EXE를 실행한 Windows 계정의 권한**으로 실행됩니다. 일반 명령 작업에는 관리자 권한이 필요하지 않습니다. SSH 사용자 이름 `vdi`는 접속용 별칭이며 Windows 계정을 전환하지 않습니다.

## 빠른 시작

아래는 VDI 주소가 `10.20.30.40`, 접속 PC 주소가 `10.20.30.50`인 예시입니다. 실제 환경의 IP로 바꿔 사용하세요.

### 1. 접속 PC에서 SSH 키 생성

접속할 사내 PC의 PowerShell에서 실행합니다.

```powershell
New-Item -ItemType Directory -Force -Path "$env:USERPROFILE\.ssh" | Out-Null
ssh-keygen -t ed25519 -f "$env:USERPROFILE\.ssh\vdi_access" -C "vdi-access"
```

키 생성 중 암호를 설정할 수 있습니다. 같은 이름의 키가 있다면 덮어쓰지 말고 다른 이름을 사용하세요. 생성된 **`vdi_access.pub` 공개키만 VDI에 복사**합니다. 개인키 `vdi_access`는 접속 PC에 보관합니다.

### 2. VDI에서 다운로드 및 실행

공개 저장소이므로 GitHub 로그인 없이 조회·다운로드·clone할 수 있습니다. VDI의 PowerShell에서 실행합니다.

```powershell
git clone https://github.com/cwj980119/vdi-ssh.git
cd vdi-ssh
Expand-Archive -LiteralPath .\distribution\VDI-SSH-windows-x64.zip -DestinationPath .\app -Force
cd .\app\VDI-SSH
```

Git을 사용할 수 없다면 위의 [실행 파일 ZIP](https://github.com/cwj980119/vdi-ssh/raw/refs/heads/main/distribution/VDI-SSH-windows-x64.zip)을 내려받아 압축을 풀고, `vdi-ssh.exe`가 있는 폴더에서 PowerShell을 열면 됩니다.

복사한 `vdi_access.pub`를 EXE와 같은 폴더에 놓고 최초 한 번 초기 설정합니다.

```powershell
.\vdi-ssh.exe init --public-key .\vdi_access.pub --listen 10.20.30.40:2222 --allow-ip 10.20.30.50
.\vdi-ssh.exe screen-init --listen 10.20.30.40:8443
.\Start-VDI-Host.cmd
```

`screen-init`은 화면용 HTTPS 인증서와 32바이트 접근 토큰을 `%APPDATA%\VDISSH`에 생성하고, 접속 URL을 표시합니다. 이 URL에는 토큰이 포함되므로 다른 사람에게 공유하지 마세요. `Start-VDI-Host.cmd`를 더블 클릭하거나 `Start-VDI-Host.ps1`을 실행하면 SSH, 파일 전송, 화면 서버가 함께 시작됩니다. 서버 창은 실행 상태로 유지하며 `Ctrl+C`로 종료할 수 있습니다.

### 3. 사내 PC에서 VDI 접속

다시 **접속 PC**의 PowerShell에서 실행합니다.

```powershell
ssh -o IdentitiesOnly=yes -i "$env:USERPROFILE\.ssh\vdi_access" -p 2222 vdi@10.20.30.40
```

최초 접속 시 표시되는 호스트 키 지문이 VDI 서버 창의 지문과 같은지 확인합니다. 접속 후 `whoami`, `Get-Location`, `Get-Process` 등의 PowerShell 명령을 실행할 수 있습니다. `exit`로 접속을 종료합니다.

명령 하나만 실행할 수도 있습니다.

```powershell
ssh -o IdentitiesOnly=yes -i "$env:USERPROFILE\.ssh\vdi_access" -p 2222 vdi@10.20.30.40 "Get-Date; whoami"
```

## 화면 공유와 원격 제어

호스트 창에 표시된 아래 형태의 URL을 접속 PC 브라우저에서 엽니다.

```text
https://10.20.30.40:8443/#화면-접근-토큰
```

자체 서명 인증서를 사용하므로 처음 한 번 브라우저 경고가 표시됩니다. 접속 주소와 지문을 VDI 호스트 화면에서 확인한 뒤 진행하세요. 화면은 4fps JPEG 스트림이며, **원격 제어 켜기**를 누른 뒤 클릭·키보드 입력이 VDI의 현재 활성 데스크톱으로 전송됩니다. UAC 보안 데스크톱과 더 높은 권한의 창은 Windows UIPI 정책에 따라 캡처 또는 제어되지 않을 수 있습니다.

## 파일 전송: SFTP와 SCP

SFTP는 SSH와 같은 키·포트·사용자 이름을 사용합니다. 초기 폴더는 VDI 계정의 홈 폴더입니다.

```powershell
sftp -o IdentitiesOnly=yes -i "$env:USERPROFILE\.ssh\vdi_access" -P 2222 vdi@10.20.30.40
# SFTP 프롬프트에서: put .\report.xlsx

scp -o IdentitiesOnly=yes -i "$env:USERPROFILE\.ssh\vdi_access" -P 2222 .\report.xlsx vdi@10.20.30.40:.
```

최신 OpenSSH의 `scp`는 SFTP 프로토콜을 사용합니다. 레거시 `scp -O` 프로토콜은 지원하지 않습니다.

## VS Code Remote SSH

접속 PC의 VS Code에 **Remote - SSH** 확장을 설치한 후, `%USERPROFILE%\.ssh\config`에 추가합니다.

```text
Host company-vdi
    HostName 10.20.30.40
    Port 2222
    User vdi
    IdentityFile C:\\Users\\내사용자\\.ssh\\vdi_access
    IdentitiesOnly yes
```

VS Code에서 `Remote-SSH: Connect to Host...`를 실행하고 `company-vdi`를 선택합니다. 플랫폼을 묻는 경우 **Windows**를 선택합니다. Remote SSH는 VDI 안에 VS Code Server를 설치할 수 있어야 하므로, VDI의 디스크 쓰기 권한과 VS Code Server 다운로드 또는 로컬 다운로드 후 SFTP 전송 경로가 필요합니다. 이 프로그램은 VS Code Server와의 통신에 필요한 VDI loopback 포트 전달만 허용합니다.

VS Code의 공식 Windows 호스트 지원은 Windows OpenSSH Server 기준입니다. 이 구현은 동일한 SSH/SFTP 및 loopback forwarding 경로를 제공하지만, 회사 VDI의 보안 정책과 VS Code 버전에서 실제 연결을 한 번 확인해야 합니다. 문제가 생기면 VS Code 설정에 다음을 추가해 Windows 플랫폼을 명시하세요.

```json
"remote.SSH.remotePlatform": { "company-vdi": "windows" }
```

## 설정과 키 보관

기본 설정은 `%APPDATA%\VDISSH`에 저장됩니다.

| 파일 | 용도 |
|---|---|
| `config.json` | 수신 주소, SSH 별칭, 허용 IP, 시작 폴더 |
| `authorized_keys` | 접속을 허용할 공개키 |
| `host_ed25519` | 서버 개인 호스트 키 |
| `audit.jsonl` | 접속과 세션 기록 |
| `screen_cert.pem`, `screen_key.pem` | 화면 서버 TLS 인증서와 개인키 |

개인키와 운영 설정은 Git 저장소에 올리지 않습니다. IP를 변경할 때는 `config.json`을 편집하고 서버를 재시작합니다. `init`은 기존 설정을 덮어쓰지 않습니다. 여러 PC를 허용하려면 초기 설정 때 `--allow-ip 10.20.30.50,10.20.30.51`을 지정할 수 있습니다.

## 연결 진단

VDI에서 실행 환경과 주소를 확인합니다.

```powershell
.\vdi-ssh.exe doctor
```

서버가 실행 중인 상태에서 접속 PC의 TCP 연결을 확인합니다.

```powershell
Test-NetConnection -ComputerName 10.20.30.40 -Port 2222
```

| 증상 | 확인할 항목 |
|---|---|
| TCP 연결 실패 | 서버 실행 여부, VDI IP, 라우팅, 방화벽의 TCP 2222 허용 |
| TCP 연결 후 바로 끊김 | `allow_from`에 실제 접속 PC의 IP가 있는지 |
| `Permission denied (publickey)` | 별칭 `vdi`, 지정한 개인키와 등록한 공개키의 일치 여부 |
| 대화형 셸만 실패 | Windows 버전, ConPTY, 서버의 `session_start_failed` 로그 |

프로그램은 방화벽, Windows 서비스, 자동 시작 설정을 변경하지 않습니다. TCP `2222`(SSH/SFTP)와 `8443`(화면 HTTPS)를 회사 정책에 맞게 접속 PC의 IP에만 허용해야 할 수 있습니다. Windows 로그오프나 재부팅 후에는 `Start-VDI-Host.cmd`를 다시 실행하세요. VDI 클라이언트를 닫았을 때 세션이 유지되는지는 해당 VDI 정책에 따릅니다. 추가 진단과 제약은 [상세 설명서](README.ko.md)를 참고하세요.

## 업데이트

실행 중인 서버를 `Ctrl+C`로 종료한 후, 저장소 최상위 폴더에서 실행합니다.

```powershell
git pull --ff-only
Expand-Archive -LiteralPath .\distribution\VDI-SSH-windows-x64.zip -DestinationPath .\app -Force
.\app\VDI-SSH\Start-VDI-Host.cmd
```

기존 설정과 호스트 키는 유지됩니다. `init`을 다시 실행할 필요가 없습니다.

## 개발

Windows와 Go 1.26 이상이 있는 개발 환경에서 다음을 실행합니다.

```powershell
.\scripts\build.ps1
```

빌드 스크립트는 검사·통합 테스트를 실행하고 `dist`와 `distribution`의 배포본을 갱신합니다. 소스를 수정해 배포할 때는 변경된 ZIP과 해시도 함께 커밋합니다.

개발 PC에서 공개키 인증·접속 거부, SFTP 파일 송수신, VS Code에 필요한 VDI loopback forwarding, 한글 명령 출력, 대화형 터미널, 세션 정리, 화면 HTTPS 인증과 캡처, Windows OpenSSH 클라이언트 호환성을 포함한 통합 테스트를 통과했습니다. 실제 VDI의 네트워크와 보안 정책에서의 동작은 해당 환경에서 확인해야 합니다. 배포 EXE는 코드 서명되지 않았습니다.

## 의존성

SSH 구현에는 `golang.org/x/crypto`, Windows API 연동에는 `golang.org/x/sys`를 사용합니다. Go 런타임과 의존성의 BSD 라이선스 원문은 배포 ZIP의 `licenses` 폴더에 포함되어 있습니다.
