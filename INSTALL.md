# Sweet 설치

[README.md](README.md)의 로컬 설치 명령을 사용해. 지원 에이전트 상세는 [모체 설치 문서](docs/UPSTREAM-INSTALL.md)를 참고해. 문서의 JuliusBrussee/caveman 주소는 모체 전용이야.

포크의 원격 설치 기본 ref는 main. 릴리스 전에는 로컬 체크아웃으로 설치하고, 게시 후에는 SWEET_REF에 검증한 커밋 SHA나 포크 태그를 지정해.

Claude Code 플러그인 갱신:

```sh
claude plugin update caveman@caveman
```
