# Changelog

## 1.0.0 (2026-05-24)


### ⚠ BREAKING CHANGES

* **wire:** флаг `-obf` удалён, заменён на `-obf-profile rtpopus`. Docker env `OBF_MODE=true` заменён на `OBF_PROFILE=rtpopus`. Wire-формат не изменён — пиры на старой версии (с `-obf`) совместимы с новой (`-obf-profile rtpopus`) при одинаковом `-obf-key`.
* **wrap:** wire-формат несовместим с 0202b9b — клиент и сервер нужно обновлять вместе.
* **wrap:** wire-формат несовместим — клиент и сервер нужно обновлять вместе.

### Features

* **auth:** добавить заготовку Authenticator ([21a018f](https://github.com/samosvalishe/free-turn-proxy/commit/21a018fecdf55642228ee6e84b13f402a17d0ccd))
* Client ID шлётся всегда, симметричный wire авторизации ([3c27977](https://github.com/samosvalishe/free-turn-proxy/commit/3c27977193b7926f5a25f6855abe7d9827729da1))
* initial commit ([942d0ff](https://github.com/samosvalishe/free-turn-proxy/commit/942d0fff43c1c1e3b6ec1b990ab03e889feb5aee))
* main ветка -&gt; master ([6aaab2d](https://github.com/samosvalishe/free-turn-proxy/commit/6aaab2dbae0d9b9f92f2ad2669822a4914776f00))
* абстракция провайдера (vk + static) ([82eb6a9](https://github.com/samosvalishe/free-turn-proxy/commit/82eb6a951bbaec1ea3c926cf2d24ab43fb471d3e))
* авторизация по client-id, freeturn:// URI и подписки ([1c01a64](https://github.com/samosvalishe/free-turn-proxy/commit/1c01a645e28f0e1a1e05d5ea3e1753ef18dec70a))
* автоустановка сервера и обновление документации ([025c066](https://github.com/samosvalishe/free-turn-proxy/commit/025c066cb601ae404d0da143fd2bcb581d2eaba7))
* вырезать идентифицирующие proxy-заголовки, расширить пул имён ([7433305](https://github.com/samosvalishe/free-turn-proxy/commit/743330551106edd46f2e691da995f2e59a9c47cb))
* добавить флаг для своих DNS-серверов ([334a17f](https://github.com/samosvalishe/free-turn-proxy/commit/334a17f1afe7835956ed25a1fa39a0fa71de2f00))
* добавлены CONTRIBUTING.md и ISSUE_TEMPLATE.md ([d01229b](https://github.com/samosvalishe/free-turn-proxy/commit/d01229b6e6008a74b62ec4145937064b7b8d5505))
* идемпотентный установщик с выбором версии и обновлением ([ba46fa4](https://github.com/samosvalishe/free-turn-proxy/commit/ba46fa44a5e78a64b407344e23256a0aba8ab8b3))
* перенести DoH-резолвер, добавить флаг -dns ([4e5690c](https://github.com/samosvalishe/free-turn-proxy/commit/4e5690cd0466d591703eb2af156040c647021369))
* поддержка refactor-коммитов в release-workflow ([7d9c659](https://github.com/samosvalishe/free-turn-proxy/commit/7d9c65955625bd463ba6819a2046eee19bd50d87))


### Bug Fixes

* **bondclient:** слать Hello с реальным числом lane после фильтрации ([4ad29aa](https://github.com/samosvalishe/free-turn-proxy/commit/4ad29aafacceb38022c4cd3d45092ef4733a7885))
* **bondframe:** ограничить размер ReadFrame значением MaxChunk ([1723809](https://github.com/samosvalishe/free-turn-proxy/commit/1723809f94384719484c4ece32a8427043375bf3))
* **bondserver:** отмена при spin из-за потери lane ([6beed08](https://github.com/samosvalishe/free-turn-proxy/commit/6beed08bbf660dd5be59488ac63290858d3a2e8c))
* **bond:** ограничить pending-map в copyBondToTCP против OOM ([c08e143](https://github.com/samosvalishe/free-turn-proxy/commit/c08e1436d471b7a02912748b81aff133b94b2c09))
* **captcha/dnsdial:** начать DI-миграцию package-level логгеров ([2dcbd47](https://github.com/samosvalishe/free-turn-proxy/commit/2dcbd473b3b6ab1fd6a9225b41825ec0ce4a7e18))
* **captcha/manual:** останавливать HTTP-сервер при отмене ctx ([7aa250a](https://github.com/samosvalishe/free-turn-proxy/commit/7aa250a7b2df14739e0768e0447eb6aea27ce2c4))
* ci ([897df17](https://github.com/samosvalishe/free-turn-proxy/commit/897df17fa8a9bbf50d955647b9212c2e0e8bed62))
* ci ([671dfcd](https://github.com/samosvalishe/free-turn-proxy/commit/671dfcd63a73ba81b2f5fe3a34a878b4368ad7c9))
* **client:** применить HandshakeSem к VLESS-диалеру ([a72b390](https://github.com/samosvalishe/free-turn-proxy/commit/a72b39007fda0f8ee48af0557a84fa7013d640f1))
* **cli:** корректно обрабатывать -help/-h вместо exit 1 ([73b5d07](https://github.com/samosvalishe/free-turn-proxy/commit/73b5d079d904f49bb611a3ce25689a20437cb17e))
* **deps:** обновить x/net до v0.55.0, toolchain до go1.26.3 ([641a813](https://github.com/samosvalishe/free-turn-proxy/commit/641a8135462f98462ca0844707f04e4c81216758))
* **docker:** исправить сборку, добавить compose, убрать VLESS_BOND ([dd14bed](https://github.com/samosvalishe/free-turn-proxy/commit/dd14bed44a02a057c05ca3feb04e071d6287b634))
* **dtlsdial:** Dial использует хелпер GenerateSelfSignedCert внутри ([87ca7b0](https://github.com/samosvalishe/free-turn-proxy/commit/87ca7b019209012d9d531e6d165029b0038761db))
* **dtlsdial:** унифицировать генерацию self-signed сертификата ([59c504b](https://github.com/samosvalishe/free-turn-proxy/commit/59c504bcc0a824af9c418bf2d9cc4627c6afdd73))
* **lint:** устранить замечания golangci-lint + переход на dockers_v2 ([4440ae3](https://github.com/samosvalishe/free-turn-proxy/commit/4440ae37683722fc473b95bb1e6a1802933a36ad))
* **server:** ограниченное ожидание второго сигнала; предупреждение при выключенном -wrap ([e33f143](https://github.com/samosvalishe/free-turn-proxy/commit/e33f143062b88b275e23265158da6639069253ba))
* **udprelay:** ctx-aware jitter-паузы; учёт listener в WaitGroup; всплытие ошибки записи DTLS ([717b224](https://github.com/samosvalishe/free-turn-proxy/commit/717b224372f16e3c88c5b8cad7f2895593bbdef1))
* **udprelay:** инкремент ConnectedStreams до ResetErrors ([8d27941](https://github.com/samosvalishe/free-turn-proxy/commit/8d279419d3bcbda43e8f90fe6edb0b0e007f5dca))
* **udprelay:** параллельный старт стримов ([100aab3](https://github.com/samosvalishe/free-turn-proxy/commit/100aab30356520c42443f8e7f3f15c9fb0a6737b))
* **udprelay:** синхронизировать watcher-горутину с возвратом Run ([ca68364](https://github.com/samosvalishe/free-turn-proxy/commit/ca683640ec47065edb41c5e9df29fd9c4ff88e23))
* исправить баги, sentinel-ошибки, устаревшие доки ([bbce69c](https://github.com/samosvalishe/free-turn-proxy/commit/bbce69c32b8f74e0220587d490ac7fcf8a1b8255))
* описание флагов ([56fc1d3](https://github.com/samosvalishe/free-turn-proxy/commit/56fc1d3890256e3574fbb1516396b08ad02f128c))
* правки после рефакторинга ([c19be2c](https://github.com/samosvalishe/free-turn-proxy/commit/c19be2c46c5df10444fea1e5ed2561fdaa1cb191))
* устранить замечания комплексной проверки ([0dfd70a](https://github.com/samosvalishe/free-turn-proxy/commit/0dfd70a5fd95beff93a00de084e571cc607ca055))
* утечки, уровни logx, обход логгера ([3a5ebc3](https://github.com/samosvalishe/free-turn-proxy/commit/3a5ebc31d5642640e1aa091762d725b91077eda7))
* форматирование ([e134893](https://github.com/samosvalishe/free-turn-proxy/commit/e134893df368b1acd563e63e6e11d75824cf4027))


### Performance

* **bondserver:** убрать аллокацию snapshotLanes на каждый retry записи ([aaeb414](https://github.com/samosvalishe/free-turn-proxy/commit/aaeb4148351a4dd4a02c68638d0dc0a5d4d4d66d))
* **bond:** убрать аллокацию на чанк в copyTCPToBond ([227c37d](https://github.com/samosvalishe/free-turn-proxy/commit/227c37dceb82891cf8c14dc6ada3e8cf0feb4e80))
* убрать аллокации на горячем пути, TCP DPI-split и KCP FEC ([7a0f98f](https://github.com/samosvalishe/free-turn-proxy/commit/7a0f98f7300a0ecc4ff0d4c51df35fd3293f225a))


### Refactoring

* **bondframe:** вынести Reorder; разделить между bondclient и bondserver ([0820615](https://github.com/samosvalishe/free-turn-proxy/commit/0820615ba64da1e4a7c21c83a032f9b06f17ef70))
* **bondserver:** tenant-scoped ключ Registry ([7a9dbe3](https://github.com/samosvalishe/free-turn-proxy/commit/7a9dbe38d15bdb941a0e37779be6c83d6cb5f8ff))
* **captcha:** вынести ручной flow в internal/client/captcha/manual ([2856155](https://github.com/samosvalishe/free-turn-proxy/commit/285615529b90e69251ebfe0e94433fb9d641e001))
* **client:** убрать deprecated-теги с package-level логгеров ([aff0215](https://github.com/samosvalishe/free-turn-proxy/commit/aff0215b3191536fc811f56599e196590a9c4a59))
* **config:** единообразные имена переменных флагов, ужать help-текст ([aa87608](https://github.com/samosvalishe/free-turn-proxy/commit/aa87608acd0ab10e5b72ad7ec52d4d00bbbe6e66))
* **config:** сгруппировать опции по доменам ([408654e](https://github.com/samosvalishe/free-turn-proxy/commit/408654e5a01ecb4b5a9887f0deccd852877715dc))
* **config:** убрать флаги -no-dtls и серверный -vless-bond ([1dfef37](https://github.com/samosvalishe/free-turn-proxy/commit/1dfef37577cbd29de69b7da52c1aa2017014fa55))
* **kcptun:** передавать Profile/FEC явно через config вместо process-wide env ([5ec3c65](https://github.com/samosvalishe/free-turn-proxy/commit/5ec3c652d99dfdd14ce86da1f7b140d85febd5c5))
* **layout:** переезд в cmd/, свернуть client/internal в internal/client ([664e744](https://github.com/samosvalishe/free-turn-proxy/commit/664e74433f2ff736821edbc48e32549d1ccb5c4b))
* **layout:** переименовать пакеты (split wire/transport/proxy) ([a2bfab1](https://github.com/samosvalishe/free-turn-proxy/commit/a2bfab1e98f2baafe3315e6b4de54d79216b6431))
* **logging:** унифицировать stdlib log.* в logx по client/cmd ([6044c4f](https://github.com/samosvalishe/free-turn-proxy/commit/6044c4fa65f3d68b3327f749701b2dcdee779215))
* **logx:** заменить Deps{Debug,Debugf} на интерфейс logx.Logger ([49fa1ef](https://github.com/samosvalishe/free-turn-proxy/commit/49fa1ef1211983cbeab266ab89c1195a76ea3f19))
* **netconn:** вынести BiCopy; использовать в tcpfwd и tcpfwdserver ([590685f](https://github.com/samosvalishe/free-turn-proxy/commit/590685f51d8197fcf8c3208afb40e6a9d19326bb))
* **provider/vk:** перенести vkauth/captcha/browserprofile/namegen под provider/vk/internal ([07e2c70](https://github.com/samosvalishe/free-turn-proxy/commit/07e2c709f67630484a2514fdcdb3d5f7f0eb885e))
* **provider:** убрать static-провайдер, оставить абстракцию ([b6c44d0](https://github.com/samosvalishe/free-turn-proxy/commit/b6c44d0f924ecce89d605baca9cdf587f4a477ee))
* **proxy:** вынести общие хелперы (минимальный объём) ([2b1c586](https://github.com/samosvalishe/free-turn-proxy/commit/2b1c586f38341f67d64111f8f25f6444fea6fc5e))
* **tcpfwd:** заменить busy-loop poll пула на Ready-канал; тихий accept-цикл при shutdown ([3cbf66c](https://github.com/samosvalishe/free-turn-proxy/commit/3cbf66c1dd3552a62dd3e858106774658f20fe5b))
* **udprelay:** разбить на run/loop/listener ([11467ff](https://github.com/samosvalishe/free-turn-proxy/commit/11467ff7c12d5a0cc47c298c74519e778aebc23c))
* **vkauth:** разбить token.go на файлы по шагам ([c5c49dd](https://github.com/samosvalishe/free-turn-proxy/commit/c5c49dd0c49a51f6110caf30a6175e222f377be5))
* **wire:** переименовать srtpmimicry → rtpopus, заменить bool -obf на -obf-profile ([879714e](https://github.com/samosvalishe/free-turn-proxy/commit/879714e7052564898e1d96117e7719c929d4a0b0))
* **wrap:** заменить DTLS-мимикрию на noise-only AEAD ([978200d](https://github.com/samosvalishe/free-turn-proxy/commit/978200dae5ec8f54b9e3a627078639d4f8c692f3))
* **wrap:** перейти на мимикрию под SRTP в обход content-фильтра VK TURN ([6bd39fe](https://github.com/samosvalishe/free-turn-proxy/commit/6bd39fed7d126e019f111b7723f8b55e5e688940))
* **wrap:** переписать как мимикрию под DTLS 1.2 ApplicationData с AEAD ([db660b4](https://github.com/samosvalishe/free-turn-proxy/commit/db660b4c9400dd82633421a679ddc1dc3769190e))
* вынести bond-клиент в internal/bond/client ([75dfa1c](https://github.com/samosvalishe/free-turn-proxy/commit/75dfa1c9d04b89a692232f908436f941949464b9))
* вынести bond-сервер в internal/bond/server ([d235a87](https://github.com/samosvalishe/free-turn-proxy/commit/d235a872edc384a6ae0c2cecbfce2f98d6d84ecc))
* вынести namegen в internal-пакет, расширить пулы имён ([b3f81ad](https://github.com/samosvalishe/free-turn-proxy/commit/b3f81add0eb27cd46b81e89be07c97b77325d52c))
* вынести stats, netadapt, bond в internal/ ([60029cc](https://github.com/samosvalishe/free-turn-proxy/commit/60029cc9cbbec94c32f02a4fc86a0ab4685c916b))
* вынести turnpipe и dtlsdial в internal/ ([14961eb](https://github.com/samosvalishe/free-turn-proxy/commit/14961eb8e1de98ff9f7b2a870761fde1d9ce5216))
* вынести UDP proxy-цикл в internal/proxy/udp ([ebb0b86](https://github.com/samosvalishe/free-turn-proxy/commit/ebb0b8688dea187d9a117162e8ec061fa62890fb))
* вынести VK-авторизацию в client/internal/vkauth ([e4a8fcb](https://github.com/samosvalishe/free-turn-proxy/commit/e4a8fcb1c4683fd81d686e94825746af564be029))
* вынести VLESS-режим в internal/proxy/vless ([0692f0a](https://github.com/samosvalishe/free-turn-proxy/commit/0692f0a50a7f086e62e709f1a3b9be2361b26519))
* вынести wrap в internal/wrap ([6e61e38](https://github.com/samosvalishe/free-turn-proxy/commit/6e61e388f2edeb275e3b5929d440bd6ecd876cee))
* вынести разбор CLI в internal/config ([72c9bbb](https://github.com/samosvalishe/free-turn-proxy/commit/72c9bbb0f86bd3c76bac9048950d07c2d3700114))
* вынести солвер captcha в internal/captcha ([ac3a603](https://github.com/samosvalishe/free-turn-proxy/commit/ac3a6031d01b0d4d1c93ccee8b963b33cb39f018))
* переименовать флаги и поля CLI/конфига ([0ea080c](https://github.com/samosvalishe/free-turn-proxy/commit/0ea080c081b7447f7eade096a431d6bdb4b8ee8f))
* симметрия, вынос серверных хендлеров ([3fc4244](https://github.com/samosvalishe/free-turn-proxy/commit/3fc4244cc6a17457b7dba37dee4879957d48808f))
* убрать суффикс V2 из солвера captcha ([130d5e9](https://github.com/samosvalishe/free-turn-proxy/commit/130d5e9298518d79359462981491fc2924faed5e))
* удалить slider POC путь captcha ([5137ddd](https://github.com/samosvalishe/free-turn-proxy/commit/5137ddd80b6a54e5f63730d4155a43db6672657d))
* удалить v1-солвер captcha и осовременить стиль ([1c6b7a8](https://github.com/samosvalishe/free-turn-proxy/commit/1c6b7a89ca42a8089e53e811637bdfba04950479))
* удалить пакет internal/auth ([db2f327](https://github.com/samosvalishe/free-turn-proxy/commit/db2f3272eaf6092e7da441ff5fcf85ff86a1eb61))
* удалить поддержку Yandex Telemost и мёртвый код ([ead97d0](https://github.com/samosvalishe/free-turn-proxy/commit/ead97d0089df3a2962f9e54168ad052616c3e807))
* унифицировать логирование в internal/proxy/* через logx.Logger ([fd31863](https://github.com/samosvalishe/free-turn-proxy/commit/fd31863373c74e9ffae459c68dc461ee9d0ca59d))

## Changelog

All notable changes to this project are documented here.

This file is maintained automatically by
[Release Please](https://github.com/googleapis/release-please) based on
[Conventional Commits](https://www.conventionalcommits.org/).
