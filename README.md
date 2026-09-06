# MAX — agente minimalista en Go

Agente de código mínimo, rápido y personalizable. Modo terminal y modo web en un solo binario.

## Dependencias
Solo `gopkg.in/yaml.v3`. El resto es stdlib. Binario ≈ 10 MB.

## Compilar
```sh
go build -o max .
```

## Usar

### Con llama.cpp (o cualquier API compatible OpenAI)
```sh
llama-server -m <modelo>.gguf --port 8080
./max
```

### Modo web
```sh
./max -mode web
# abrí http://localhost:8090
```

### APIs remotas (openrouter, groq, etc.)
```sh
./max -base-url https://openrouter.ai/api/v1 -api-key $OPENROUTER_KEY
```

## Configuración
```sh
cp config.example.yaml max.yaml   # editá y listo
```
También se puede editar `prompts/default.md` para cambiar la personalidad del agente, o apuntar `system_prompt` a otro archivo.

## Herramientas
| herramienta      | descripción                         | peligrosa |
|------------------|-------------------------------------|-----------|
| run_command      | ejecuta comandos shell              | sí        |
| write_file       | escribe archivos                    | sí        |
| read_file        | lee archivos (con rango opcional)   | no        |
| list_dir         | lista directorios                   | no        |
| search_files     | grep en el proyecto                 | no        |

Las herramientas peligrosas piden confirmación (`y/N`). Con `-yes` o `auto_approve: true` se ejecutan solas. Si el stdin no es un terminal (pipe), se auto-aprueban.

### API de herramientas nativas + fallback de code-fences
MAX usa `tool_calls` nativos cuando el servidor/modelo los soporta, y además un **fallback que funciona con cualquier modelo**: si la respuesta no trae `tool_calls` pero tiene comandos dentro de bloques ```bash``` (según el system prompt), MAX los extrae, los manda por el mismo flujo de aprobación/ejecución y le devuelve la salida al modelo para continuar. Esto hace que un llama-server local con un modelo 3B sin tool-support real también pueda trabajar.

## Comandos TUI
`/help` `/tools` `/clear` `/exit`

## Flags
```
-mode tui|web      MODELO de interfaz (default: tui)
-config ARCHIVO    config YAML (default: max.yaml)
-model NOMBRE      sobreescribe el modelo
-base-url URL      URL base /v1
-api-key CLAVE     clave de API
-server DIR        dirección del modo web (default :8090)
-yes               auto-aprobar herramientas
-no-tools          desactivar herramientas
```

## Test
```sh
go test ./...
```
Nota: el puerto 18080 del test de humo corre un LLM simulado (`/tmp/opencode/mockllm`).