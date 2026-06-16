# obs-pktm

Replica inicial en Go puro del temporizador de `obs-scene-guard.ahk`.

## Estructura

- `main.go`: conecta acciones de la app.
- `internal/hotkeys`: escucha global de `F9`, `F10` y `F12` con `WH_KEYBOARD_LL`.
- `internal/timer`: overlay Win32 del contador.
- `internal/winmsg`: loop de mensajes Win32 compartido.

## Comportamiento

- `F9`: inicia un contador global de 10 minutos y deja pasar la tecla a la app activa.
- `F10`: detiene y oculta el contador, tambien dejando pasar la tecla.
- `F12`: cierra el proceso.
- Al llegar a cero, muestra `FIN` a pantalla completa.

La escucha de teclas usa `WH_KEYBOARD_LL` de la API de Windows en vez de `RegisterHotKey`, para no reservar F9/F10 y permitir que OBS siga recibiendo esas teclas.

## Ejecutar

Con Go instalado en Windows:

```powershell
go run .
```

Para compilar un `.exe` sin ventana de consola:

```powershell
go build -ldflags="-H windowsgui" -o obs-pktm.exe .
```
