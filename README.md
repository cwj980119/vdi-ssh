# VDI SSH

설치 없이 실행하는 Windows VDI용 SSH 서버입니다. 공개키로 인증하고 대화형 PowerShell 또는 개별 명령을 실행할 수 있습니다. VDI에 OpenSSH 서버나 별도 개발 런타임을 설치하지 않아도 됩니다.

**[상세 설정·접속 방법 → README.ko.md](README.ko.md)**

## Git으로 받아 실행하기

비공개 저장소이므로 GitHub 계정의 저장소 접근 권한이 필요합니다. VDI의 PowerShell에서 처음 한 번 실행합니다.

```powershell
git clone https://github.com/cwj980119/vdi-ssh.git
cd vdi-ssh
Expand-Archive -LiteralPath .\distribution\VDI-SSH-windows-x64.zip -DestinationPath .\app -Force
cd .\app\VDI-SSH
.\vdi-ssh.exe help
```

`distribution`에 빌드된 Windows x64 실행 파일 ZIP과 SHA-256 해시를 포함했습니다. 직접 빌드할 필요가 없습니다. ZIP에는 사용 설명서와 의존성 라이선스도 들어 있습니다.

실제로 접속하려면 접속 PC에서 만든 공개키 `.pub`를 VDI로 복사하고, 아래 IP를 실제 주소로 바꿔 초기 설정합니다.

```powershell
# 예시: VDI=10.20.30.40, 접속 PC=10.20.30.50
.\vdi-ssh.exe init --public-key .\vdi_access.pub --listen 10.20.30.40:2222 --allow-ip 10.20.30.50
.\vdi-ssh.exe serve
```

클라이언트 키 생성과 접속 명령은 [상세 설명서](README.ko.md)를 참고하십시오. 서버는 EXE를 실행한 Windows 계정의 권한으로 명령을 수행합니다. 기본 설정과 서버 키는 `%APPDATA%\VDISSH`에 보관되며, Git 저장소에 넣지 않습니다.

## 업데이트

실행 중인 서버를 `Ctrl+C`로 종료한 후, 저장소 최상위 폴더에서 실행합니다.

```powershell
git pull --ff-only
Expand-Archive -LiteralPath .\distribution\VDI-SSH-windows-x64.zip -DestinationPath .\app -Force
.\app\VDI-SSH\vdi-ssh.exe serve
```

기존 설정과 호스트 키는 유지됩니다. `init`을 다시 실행할 필요가 없습니다.

## 개발

Windows와 Go 1.26 이상이 있는 개발 환경에서 다음을 실행합니다.

```powershell
.\scripts\build.ps1
```

빌드 스크립트는 검사·통합 테스트를 실행하고 `dist`와 `distribution`의 배포본을 갱신합니다. 소스를 수정해 배포할 때는 변경된 ZIP과 해시도 함께 커밋합니다.

현재 범위는 SSH 터미널과 명령 실행입니다. 화면 제어, SFTP/SCP, 포트 포워딩, VS Code Remote SSH는 지원하지 않습니다. 실제 VDI의 네트워크와 보안 정책에서 실행 가능한지는 해당 환경에서 확인해야 합니다.
