# Changelog

## [1.4.3](https://github.com/samosvalishe/free-turn-proxy/compare/v1.4.2...v1.4.3) (2026-06-22)


### Bug Fixes

* bump captcha ([b0c3e65](https://github.com/samosvalishe/free-turn-proxy/commit/b0c3e65c40bed7e09610d5a4a49d322fa6d55fea))
* OBF_TIMING -&gt; docker ([d8f0e7e](https://github.com/samosvalishe/free-turn-proxy/commit/d8f0e7e99554372a4b6dd057e760509e8a65bece))

## [1.4.2](https://github.com/samosvalishe/free-turn-proxy/compare/v1.4.1...v1.4.2) (2026-06-22)


### Bug Fixes

* captcha ([d73f426](https://github.com/samosvalishe/free-turn-proxy/commit/d73f42603aacefa3ec08d43771a073393ae0500b))

## [1.4.1](https://github.com/samosvalishe/free-turn-proxy/compare/v1.4.0...v1.4.1) (2026-06-19)


### Bug Fixes

* оставлены только VK app_id с доступом к calls.getAnonymousToken ([ebb1f6b](https://github.com/samosvalishe/free-turn-proxy/commit/ebb1f6bf0702217aca2ae54ab8d2115d93f85a95))


### Refactoring

* единая константа версии VK API для calls.* ([b76e212](https://github.com/samosvalishe/free-turn-proxy/commit/b76e21266d3a8bb457ad40263da2404089a2b7d6))

## [1.4.0](https://github.com/samosvalishe/free-turn-proxy/compare/v1.3.2...v1.4.0) (2026-06-19)


### Features

* мультиплексирование звонков ([#21](https://github.com/samosvalishe/free-turn-proxy/issues/21)) ([1c58325](https://github.com/samosvalishe/free-turn-proxy/commit/1c583258627f5b847fee62d403a50270ac9e1509))


### Bug Fixes

* -obf-timing допустим только с -mode udp и включённой обфускацией ([e50febc](https://github.com/samosvalishe/free-turn-proxy/commit/e50febc834544fcb47eb44cf7edc0f7930199066))
* потокобезопасность send-состояния obf-кодеков rtpopus3 и shape ([9564f34](https://github.com/samosvalishe/free-turn-proxy/commit/9564f3475f5d5926ec75bcb9fd840613a52e404d))
* устранены блокеры линтера в obf/multi ([bafdcdd](https://github.com/samosvalishe/free-turn-proxy/commit/bafdcdd027c29ee52f605af2d6d83acf9827855d))

## [1.3.2](https://github.com/samosvalishe/free-turn-proxy/compare/v1.3.1...v1.3.2) (2026-06-17)


### Bug Fixes

* **dnsdial:** вернуть простую UDP-пробу auto до auto-пробы реального хоста ([7d1ae10](https://github.com/samosvalishe/free-turn-proxy/commit/7d1ae10a473dcd030a2e9b3261a559038fda8bbd))

## [1.3.1](https://github.com/samosvalishe/free-turn-proxy/compare/v1.3.0...v1.3.1) (2026-06-17)


### Bug Fixes

* **dnsdial:** не вешать старт auto на DoH-пробу ([1ec4fd4](https://github.com/samosvalishe/free-turn-proxy/commit/1ec4fd463dbdd2312ccfdd2caf1e9b5e4dc98c0c))

## [1.3.0](https://github.com/samosvalishe/free-turn-proxy/compare/v1.2.0...v1.3.0) (2026-06-17)


### Features

* **dnsdial:** auto-проба реального хоста с DoH recovery ([1ec2b26](https://github.com/samosvalishe/free-turn-proxy/commit/1ec2b2610a40b29bcef52f3ebe71928d186897c9))
* **netconn:** анти-DPI мульти-сплит ClientHello по SNI ([cc40510](https://github.com/samosvalishe/free-turn-proxy/commit/cc405100ab97f03b21521ea17b5daf78aef94116))
* **vkauth:** флаг -browser и профиль Firefox ([69a68bb](https://github.com/samosvalishe/free-turn-proxy/commit/69a68bbc3189e296d274ea7dcee3629507189e03))
* **wire:** профиль rtpopus2 и интерфейс Codec ([f8e840c](https://github.com/samosvalishe/free-turn-proxy/commit/f8e840ccdf9762b1582cbd2c3b33a5326738138a))


### Bug Fixes

* **vkauth:** группировка кэша по 1-based streamID ([745539d](https://github.com/samosvalishe/free-turn-proxy/commit/745539dc114c79a2a46c33fb776c5d16ae961d9b))
* миграция на ru домен ([4084cce](https://github.com/samosvalishe/free-turn-proxy/commit/4084cce0911cf3a00a4bd03206d5c3e0721c16b6))

## [1.2.0](https://github.com/samosvalishe/free-turn-proxy/compare/v1.1.1...v1.2.0) (2026-06-11)


### Features

* **uri:** переход на base64url(json), расширенные параметры ([402606e](https://github.com/samosvalishe/free-turn-proxy/commit/402606e4f747c9c78d0c8389815f9e320c0480b1))

## [1.1.1](https://github.com/samosvalishe/free-turn-proxy/compare/v1.1.0...v1.1.1) (2026-06-11)


### Bug Fixes

* **dnsdial:** убрать динамический фоллбэк на DoH ([40796b4](https://github.com/samosvalishe/free-turn-proxy/commit/40796b48449e184d8eea31d822b6461f206cd815))

## [1.1.0](https://github.com/samosvalishe/free-turn-proxy/compare/v1.0.3...v1.1.0) (2026-06-11)


### Features

* **turn:** фоллбэк по нескольким relay-адресам при allocate ([40000fd](https://github.com/samosvalishe/free-turn-proxy/commit/40000fde5d82023f8552e2a0255b6ca50324b8ed))


### Bug Fixes

* **dnsdial:** динамический фоллбэк на DoH при отказе UDP/53 после пробы ([f24c541](https://github.com/samosvalishe/free-turn-proxy/commit/f24c5416de91349fd6a0332cb4e4744da4ca95fc))
* **install:** неинтерактивный apt, чтобы needrestart/debconf не вешали установку ([b518dc4](https://github.com/samosvalishe/free-turn-proxy/commit/b518dc40b23f34942d684cbb7422fef9ddda28b3))
* **turndial:** детект блэкхола по ChannelBind, не CreatePermission refresh ([2534bbe](https://github.com/samosvalishe/free-turn-proxy/commit/2534bbe96ae22af9fc17c322b337aa37ae77f086))
* **udprelay:** детект блэкхола permission по логу pion вместо трафик-эвристики ([106a485](https://github.com/samosvalishe/free-turn-proxy/commit/106a48533cfab863c09e08ff7cbac4868736e69e))

## [1.0.3](https://github.com/samosvalishe/free-turn-proxy/compare/v1.0.2...v1.0.3) (2026-06-04)


### Bug Fixes

* bump go toolchain ([3cb39d4](https://github.com/samosvalishe/free-turn-proxy/commit/3cb39d40f6e630b638d0ce44398da7022ceb2353))
* **captcha:** обновить captchaScriptVersion до 1.1.1348 ([1ff8dd9](https://github.com/samosvalishe/free-turn-proxy/commit/1ff8dd9cbaaa79858aaa4be68a404d89252480db))
* **client:** сохранять client_id в доступный для записи каталог ([3bdc8e8](https://github.com/samosvalishe/free-turn-proxy/commit/3bdc8e815af871feeea53946964917eaa3a6c0ce))
* **udprelay:** рециклить стрим при блэкхоле TURN permission ([a337bb0](https://github.com/samosvalishe/free-turn-proxy/commit/a337bb0a634bba6783891f2c085a31123353c496))
* **vkauth:** фрагментировать ClientHello на control plane ([56da9fe](https://github.com/samosvalishe/free-turn-proxy/commit/56da9fecdc5603c14073cd8632338d0c0f1b27f5))

## [1.0.2](https://github.com/samosvalishe/free-turn-proxy/compare/v1.0.1...v1.0.2) (2026-06-01)


### Bug Fixes

* **captcha:** обновить captchaScriptVersion до 1.1.1346 ([5fc2a09](https://github.com/samosvalishe/free-turn-proxy/commit/5fc2a09fbb6bfcb647edae106b40946a880699d4))
* **dtlsdial:** уникальный self-signed cert на каждый handshake ([43180f0](https://github.com/samosvalishe/free-turn-proxy/commit/43180f0362d2254b5af87bccd355a89261ed39fd))
* install.sh ([4b26d63](https://github.com/samosvalishe/free-turn-proxy/commit/4b26d6361d1c32c75bc7207aa4d6de83e4aba638))
* install.sh ([854cab9](https://github.com/samosvalishe/free-turn-proxy/commit/854cab95c074f4df71185e3b0ee2ed4c05de86fa))
* install.sh ([375cf55](https://github.com/samosvalishe/free-turn-proxy/commit/375cf55182100c201e4032c68128b048c1a177fa))
* **turndial:** не переопределять PermissionRefreshInterval (дефолт pion) ([a0ea846](https://github.com/samosvalishe/free-turn-proxy/commit/a0ea846f255c5c4b13b79821a773419d60005f6b))
* **udprelay:** барьер старта стримов для прогрева кэша creds ([a97ad6c](https://github.com/samosvalishe/free-turn-proxy/commit/a97ad6ca336e5a170b499559ecf64b7d4945f3c2))


### Performance

* **udprelay:** in-place wrap/unwrap obf без копий payload ([89c390c](https://github.com/samosvalishe/free-turn-proxy/commit/89c390cef5edf22f6856143ecbc6b6939d96cf29))

## [1.0.1](https://github.com/samosvalishe/free-turn-proxy/compare/v1.0.0...v1.0.1) (2026-05-26)


### Bug Fixes

* **release:** drop package-name so root component is empty (issue 2214) ([281dd3e](https://github.com/samosvalishe/free-turn-proxy/commit/281dd3e87ac8366d7192f014f49c97c198f76ef1))
* **release:** set empty component for release-please tagging (issue 2214) ([e976f7d](https://github.com/samosvalishe/free-turn-proxy/commit/e976f7d8efdb93353fc7c7e978775974fae21863))


### Refactoring

* install.sh ([0f74c23](https://github.com/samosvalishe/free-turn-proxy/commit/0f74c238c5abafdb3bf5ce2f0139c41d62f07abd))

## 1.0.0 (2026-05-25)


### Features

* **auth:** добавить заготовку Authenticator ([65aa55b](https://github.com/samosvalishe/free-turn-proxy/commit/65aa55b211e374e485f9efd162440b900d4dcb21))
* Client ID шлётся всегда, симметричный wire авторизации ([f1b047e](https://github.com/samosvalishe/free-turn-proxy/commit/f1b047e9e558df762e240db223b9daa7767400d7))
* initial commit ([942d0ff](https://github.com/samosvalishe/free-turn-proxy/commit/942d0fff43c1c1e3b6ec1b990ab03e889feb5aee))
* main ветка -&gt; master ([7827001](https://github.com/samosvalishe/free-turn-proxy/commit/782700113e5dbc6d009800e4972bb74df6c034fe))
* абстракция провайдера (vk + static) ([12c1ad6](https://github.com/samosvalishe/free-turn-proxy/commit/12c1ad628365e71facfead1a4a550a2e20947f73))
* авторизация по client-id, freeturn:// URI и подписки ([e061345](https://github.com/samosvalishe/free-turn-proxy/commit/e0613450895a1bb98cad3b7627bcb65f4fd2251c))
* автоустановка сервера и обновление документации ([7651986](https://github.com/samosvalishe/free-turn-proxy/commit/76519860b41afc10e772e51f75a157d832b06337))
* вырезать идентифицирующие proxy-заголовки, расширить пул имён ([7433305](https://github.com/samosvalishe/free-turn-proxy/commit/743330551106edd46f2e691da995f2e59a9c47cb))
* добавить флаг для своих DNS-серверов ([334a17f](https://github.com/samosvalishe/free-turn-proxy/commit/334a17f1afe7835956ed25a1fa39a0fa71de2f00))
* добавлены CONTRIBUTING.md и ISSUE_TEMPLATE.md ([953e575](https://github.com/samosvalishe/free-turn-proxy/commit/953e5758da9b3e6d27b19c24e0fce0e60b300c6c))
* идемпотентный установщик с выбором версии и обновлением ([198d314](https://github.com/samosvalishe/free-turn-proxy/commit/198d314031b075e72c1fdfb76d72de1fe899be43))
* перенести DoH-резолвер, добавить флаг -dns ([4e5690c](https://github.com/samosvalishe/free-turn-proxy/commit/4e5690cd0466d591703eb2af156040c647021369))
* поддержка refactor-коммитов в release-workflow ([369ba1e](https://github.com/samosvalishe/free-turn-proxy/commit/369ba1e43c7b9567a11971d105bd82d952b95f72))


### Bug Fixes

* **bondclient:** слать Hello с реальным числом lane после фильтрации ([ac69560](https://github.com/samosvalishe/free-turn-proxy/commit/ac695603b759edaa37714a25bb5e7de733454164))
* **bondframe:** ограничить размер ReadFrame значением MaxChunk ([a8361f3](https://github.com/samosvalishe/free-turn-proxy/commit/a8361f3bdf6e84f4d3229f312695117cee96e8cb))
* **bondserver:** отмена при spin из-за потери lane ([f54f59b](https://github.com/samosvalishe/free-turn-proxy/commit/f54f59bff9eb53845d47f07cbb6d0fe5fe9f85f4))
* **bond:** ограничить pending-map в copyBondToTCP против OOM ([d0dabee](https://github.com/samosvalishe/free-turn-proxy/commit/d0dabeec672c4d39fe32fe5006b719c9549923e5))
* **captcha/dnsdial:** начать DI-миграцию package-level логгеров ([5735b4a](https://github.com/samosvalishe/free-turn-proxy/commit/5735b4a297114a41a16b4551543a1dbcb745a3c9))
* **captcha/manual:** останавливать HTTP-сервер при отмене ctx ([7ffa21b](https://github.com/samosvalishe/free-turn-proxy/commit/7ffa21b8aa51bb3ca01b8ed93c77e9ab4726541c))
* ci ([029c3ab](https://github.com/samosvalishe/free-turn-proxy/commit/029c3aba9528209a7d21eb214cbc1b53064f95d4))
* ci ([587e95d](https://github.com/samosvalishe/free-turn-proxy/commit/587e95dbcc9915bdfb1fe1c5e9520ebb2c2e1f62))
* ci ([b4f8cbc](https://github.com/samosvalishe/free-turn-proxy/commit/b4f8cbcec0a055e949de846bd3943e3f0b387ff1))
* ci ([dab5848](https://github.com/samosvalishe/free-turn-proxy/commit/dab58489d9b7c03ec7101ea3f9e723ba7edc8971))
* **client:** применить HandshakeSem к VLESS-диалеру ([1437601](https://github.com/samosvalishe/free-turn-proxy/commit/14376016aa2d950ae5305660e922c9651bebf427))
* **cli:** корректно обрабатывать -help/-h вместо exit 1 ([0bf46b1](https://github.com/samosvalishe/free-turn-proxy/commit/0bf46b1ceb9585bcd443130c1acb7c41974ced3c))
* **deps:** обновить x/net до v0.55.0, toolchain до go1.26.3 ([f8bc3f5](https://github.com/samosvalishe/free-turn-proxy/commit/f8bc3f54e5462fb9125b2327a8119e3e3aefb82e))
* **docker:** исправить сборку, добавить compose, убрать VLESS_BOND ([dfbfae2](https://github.com/samosvalishe/free-turn-proxy/commit/dfbfae29014b3cd246550589640e871ee424807e))
* **dtlsdial:** Dial использует хелпер GenerateSelfSignedCert внутри ([21138fe](https://github.com/samosvalishe/free-turn-proxy/commit/21138fef26b61f8ba4b72f8e5389d3b90b7026b2))
* **dtlsdial:** унифицировать генерацию self-signed сертификата ([ef549dc](https://github.com/samosvalishe/free-turn-proxy/commit/ef549dc2b8405a1e8378d9c81f2ae3871495121f))
* **install:** переписать установщик сервера — TUI, надёжность, non-interactive ([1b154a2](https://github.com/samosvalishe/free-turn-proxy/commit/1b154a2e84057c07344a4d7308ca7d26f5f127ec))
* **lint:** устранить замечания golangci-lint + переход на dockers_v2 ([dd82332](https://github.com/samosvalishe/free-turn-proxy/commit/dd8233208ccbe60ea17745ddc17df7a8e6c56872))
* **routes:** починить установку маршрутов в routes.ps1 на Windows ([7b0a907](https://github.com/samosvalishe/free-turn-proxy/commit/7b0a9076380bbcdc2f83d6da63ca3800b5d68412))
* **server:** ограниченное ожидание второго сигнала; предупреждение при выключенном -wrap ([fb711c1](https://github.com/samosvalishe/free-turn-proxy/commit/fb711c12f5e1f78ee037ceed434b6cdde5a961d8))
* **turndial:** подавить периодический CreatePermission refresh ([98e1d7c](https://github.com/samosvalishe/free-turn-proxy/commit/98e1d7cc8e0947e91892b75ecfed6cd4a41ab0ac))
* **udprelay:** ctx-aware jitter-паузы; учёт listener в WaitGroup; всплытие ошибки записи DTLS ([1459f39](https://github.com/samosvalishe/free-turn-proxy/commit/1459f3955122d3e1947d3b09ffcddaf6935e8286))
* **udprelay:** инкремент ConnectedStreams до ResetErrors ([6588544](https://github.com/samosvalishe/free-turn-proxy/commit/65885443689e3bb0d0ecda3a3394bf0e54c015b4))
* **udprelay:** параллельный старт стримов ([9090d58](https://github.com/samosvalishe/free-turn-proxy/commit/9090d5894ad801c2ab74e1a434400519894fefc6))
* **udprelay:** синхронизировать watcher-горутину с возвратом Run ([fba9a5e](https://github.com/samosvalishe/free-turn-proxy/commit/fba9a5e26d386c78b8f58af4332ddbf7e421a542))
* исправить баги, sentinel-ошибки, устаревшие доки ([ed68259](https://github.com/samosvalishe/free-turn-proxy/commit/ed6825976a3045bf427ea74532ad8604161a2700))
* описание флагов ([d292d30](https://github.com/samosvalishe/free-turn-proxy/commit/d292d3084f12d8a6cda1eb029f56b9abd9d94a79))
* правки после рефакторинга ([ada9b94](https://github.com/samosvalishe/free-turn-proxy/commit/ada9b949a43d39cd8df5e6eea7746f639a61f003))
* устранить замечания комплексной проверки ([8b2e028](https://github.com/samosvalishe/free-turn-proxy/commit/8b2e028f997c306984fa16aa86ceb4d34858ccce))
* утечки, уровни logx, обход логгера ([52e7cb8](https://github.com/samosvalishe/free-turn-proxy/commit/52e7cb83e68a1c9cd076f71fe6ef4d7ab3b1225c))
* форматирование ([e134893](https://github.com/samosvalishe/free-turn-proxy/commit/e134893df368b1acd563e63e6e11d75824cf4027))


### Performance

* **bondserver:** убрать аллокацию snapshotLanes на каждый retry записи ([9459e94](https://github.com/samosvalishe/free-turn-proxy/commit/9459e9406664cee23b7a1b1f326ee6b8a53a0364))
* **bond:** убрать аллокацию на чанк в copyTCPToBond ([085b719](https://github.com/samosvalishe/free-turn-proxy/commit/085b719be786b317eb5fa0daffa0b8905d2c29c1))
* убрать аллокации на горячем пути, TCP DPI-split и KCP FEC ([7a0f98f](https://github.com/samosvalishe/free-turn-proxy/commit/7a0f98f7300a0ecc4ff0d4c51df35fd3293f225a))


### Refactoring

* **bondframe:** вынести Reorder; разделить между bondclient и bondserver ([4a962d8](https://github.com/samosvalishe/free-turn-proxy/commit/4a962d826477ace329ae23adb66700ca0da74a6b))
* **bondserver:** tenant-scoped ключ Registry ([52271d0](https://github.com/samosvalishe/free-turn-proxy/commit/52271d0367d87219af63d552c7dbd937e422af8c))
* **captcha:** вынести ручной flow в internal/client/captcha/manual ([acbd90f](https://github.com/samosvalishe/free-turn-proxy/commit/acbd90f52e174f550ce74fc429b692cb22fd73df))
* **client:** убрать deprecated-теги с package-level логгеров ([6b8b82f](https://github.com/samosvalishe/free-turn-proxy/commit/6b8b82f5308e9ccd9424d0f833707ae1a488b77d))
* **config:** единообразные имена переменных флагов, ужать help-текст ([bf59e5c](https://github.com/samosvalishe/free-turn-proxy/commit/bf59e5c8b4b52c4aa48ba74f045e278a2348ac74))
* **config:** сгруппировать опции по доменам ([6c9bfba](https://github.com/samosvalishe/free-turn-proxy/commit/6c9bfbae821430ba99e589b23547918e3958d97b))
* **config:** убрать флаги -no-dtls и серверный -vless-bond ([5b68683](https://github.com/samosvalishe/free-turn-proxy/commit/5b68683d415043af8d8c56dbdc3c58cb01bf31c4))
* **kcptun:** передавать Profile/FEC явно через config вместо process-wide env ([483e45d](https://github.com/samosvalishe/free-turn-proxy/commit/483e45d68aeefeb1e6ff369831568d41138b8727))
* **layout:** переезд в cmd/, свернуть client/internal в internal/client ([2167ba9](https://github.com/samosvalishe/free-turn-proxy/commit/2167ba990bcdd1fd214e1d6dab79ddaab8c56dde))
* **layout:** переименовать пакеты (split wire/transport/proxy) ([1fb869b](https://github.com/samosvalishe/free-turn-proxy/commit/1fb869b3e12d0be09a4adcb55844ad9644dfdfa3))
* **logging:** унифицировать stdlib log.* в logx по client/cmd ([9d7d754](https://github.com/samosvalishe/free-turn-proxy/commit/9d7d7547402bc78fa269cefb78730ba2861732dc))
* **logx:** заменить Deps{Debug,Debugf} на интерфейс logx.Logger ([7fd2d95](https://github.com/samosvalishe/free-turn-proxy/commit/7fd2d9594d172058526580e2c64a2e1dfd3fd7eb))
* **netconn:** вынести BiCopy; использовать в tcpfwd и tcpfwdserver ([7b4d6be](https://github.com/samosvalishe/free-turn-proxy/commit/7b4d6be1d846d6be3c32254ea04dc82be7ff6491))
* **provider/vk:** перенести vkauth/captcha/browserprofile/namegen под provider/vk/internal ([2b85d93](https://github.com/samosvalishe/free-turn-proxy/commit/2b85d937ea5e3dbb53ef047523e7700f070051b5))
* **provider:** убрать static-провайдер, оставить абстракцию ([cf47f37](https://github.com/samosvalishe/free-turn-proxy/commit/cf47f374b3c57ab73b7d53405cd5ef91d3e18162))
* **proxy:** вынести общие хелперы (минимальный объём) ([e212465](https://github.com/samosvalishe/free-turn-proxy/commit/e212465f835f068455fc04724e027c119d104172))
* **tcpfwd:** заменить busy-loop poll пула на Ready-канал; тихий accept-цикл при shutdown ([2b24471](https://github.com/samosvalishe/free-turn-proxy/commit/2b24471553b7a9acb0c0117d7ce666bac758e97e))
* **udprelay:** разбить на run/loop/listener ([d82258f](https://github.com/samosvalishe/free-turn-proxy/commit/d82258f844ef65ba06ba05aa7de4130c43f2c56e))
* **vkauth:** разбить token.go на файлы по шагам ([0e2d42c](https://github.com/samosvalishe/free-turn-proxy/commit/0e2d42c9126505599db25d8c6f82956036c193be))
* **wire:** переименовать srtpmimicry → rtpopus, заменить bool -obf на -obf-profile ([4bedd00](https://github.com/samosvalishe/free-turn-proxy/commit/4bedd00662bb75a5f2fdd8fc3cee9c8eb6d94e7c))
* **wrap:** заменить DTLS-мимикрию на noise-only AEAD ([e2dd09a](https://github.com/samosvalishe/free-turn-proxy/commit/e2dd09a7296f972285850786c6552d66006bf19d))
* **wrap:** перейти на мимикрию под SRTP в обход content-фильтра VK TURN ([729557d](https://github.com/samosvalishe/free-turn-proxy/commit/729557d00d3f7b16e4d1d876adca23404eff738c))
* **wrap:** переписать как мимикрию под DTLS 1.2 ApplicationData с AEAD ([39f95a8](https://github.com/samosvalishe/free-turn-proxy/commit/39f95a8e0c2ca3520c530e425aa55a5b586feadb))
* вынести bond-клиент в internal/bond/client ([29771b1](https://github.com/samosvalishe/free-turn-proxy/commit/29771b1e273c04f35462293095646d2a03b6dd24))
* вынести bond-сервер в internal/bond/server ([dc2e413](https://github.com/samosvalishe/free-turn-proxy/commit/dc2e4135aa28aaaf38ce06bd6da011205e91d943))
* вынести namegen в internal-пакет, расширить пулы имён ([b3f81ad](https://github.com/samosvalishe/free-turn-proxy/commit/b3f81add0eb27cd46b81e89be07c97b77325d52c))
* вынести stats, netadapt, bond в internal/ ([ada0c74](https://github.com/samosvalishe/free-turn-proxy/commit/ada0c746aa47266c07f3653b785b2a4b17d55d67))
* вынести turnpipe и dtlsdial в internal/ ([bc1aea6](https://github.com/samosvalishe/free-turn-proxy/commit/bc1aea625bb8721828ae6beae4ea309da7da137d))
* вынести UDP proxy-цикл в internal/proxy/udp ([82f231d](https://github.com/samosvalishe/free-turn-proxy/commit/82f231d5e03227c0d10339e3fe0eb68f79556f44))
* вынести VK-авторизацию в client/internal/vkauth ([5bf6dd0](https://github.com/samosvalishe/free-turn-proxy/commit/5bf6dd03995440e6cede3e2452ae6d08c286a3a7))
* вынести VLESS-режим в internal/proxy/vless ([0909656](https://github.com/samosvalishe/free-turn-proxy/commit/09096568ff51fa4843f2b0bc69a52a24c1e6e3d4))
* вынести wrap в internal/wrap ([322e8db](https://github.com/samosvalishe/free-turn-proxy/commit/322e8db4fac51c2709d5eb93809155681bf73a8e))
* вынести разбор CLI в internal/config ([817def3](https://github.com/samosvalishe/free-turn-proxy/commit/817def3e0165369e5deea08a4e875c0627d14e90))
* вынести солвер captcha в internal/captcha ([ac3a603](https://github.com/samosvalishe/free-turn-proxy/commit/ac3a6031d01b0d4d1c93ccee8b963b33cb39f018))
* переименовать флаги и поля CLI/конфига ([31e35ef](https://github.com/samosvalishe/free-turn-proxy/commit/31e35efbf3c1cd5d604f250fc35992411377ec92))
* симметрия, вынос серверных хендлеров ([51bda46](https://github.com/samosvalishe/free-turn-proxy/commit/51bda46446ab57171aa13400b820fd531519cea5))
* убрать суффикс V2 из солвера captcha ([130d5e9](https://github.com/samosvalishe/free-turn-proxy/commit/130d5e9298518d79359462981491fc2924faed5e))
* удалить slider POC путь captcha ([5137ddd](https://github.com/samosvalishe/free-turn-proxy/commit/5137ddd80b6a54e5f63730d4155a43db6672657d))
* удалить v1-солвер captcha и осовременить стиль ([1c6b7a8](https://github.com/samosvalishe/free-turn-proxy/commit/1c6b7a89ca42a8089e53e811637bdfba04950479))
* удалить пакет internal/auth ([d1e8075](https://github.com/samosvalishe/free-turn-proxy/commit/d1e80751c743f1260b3cdefea98cf00897699369))
* удалить поддержку Yandex Telemost и мёртвый код ([ead97d0](https://github.com/samosvalishe/free-turn-proxy/commit/ead97d0089df3a2962f9e54168ad052616c3e807))
* унифицировать логирование в internal/proxy/* через logx.Logger ([aff95e8](https://github.com/samosvalishe/free-turn-proxy/commit/aff95e87d3ef82bac470036867e56205ced4da63))

## Changelog

All notable changes to this project are documented here.

This file is maintained automatically by
[Release Please](https://github.com/googleapis/release-please) based on
[Conventional Commits](https://www.conventionalcommits.org/).
