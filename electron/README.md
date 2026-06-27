# New API Electron Desktop App

This directory contains the Electron wrapper for New API, providing a native desktop application with system tray support for Windows, macOS, and Linux.

## Prerequisites

### 1. Build Tools
The Electron app packages the Go backend binary. Install Go, Bun, Node.js, and npm before building from source.

### 2. Electron Dependencies
```bash
cd electron
npm ci
```

## Development

Run the app in development mode:
```bash
# Terminal 1
docker compose -f docker-compose.dev.yml up -d

# Terminal 2
cd web && bun install && bun run dev

# Terminal 3
cd electron && npm ci && npm run dev-app
```

This will:
- Connect to the frontend dev server on port 3001
- Use the Go backend on port 3000
- Open an Electron window with DevTools enabled
- Create a system tray icon (menu bar on macOS)
- Store database in `../data/new-api.db`

## Building for Production

### Quick Build
```bash
cd electron
./build.sh
```

### Build Output
- Built applications are in `electron/dist/`
- macOS: `.dmg` (installer) and `.zip` (portable)
- Windows: `.exe` (installer) and portable exe
- Linux: `.AppImage` and `.deb`

## Configuration

### Port
Default port is 3000. To change, edit `main.js`:
```javascript
const PORT = 3000; // Change to desired port
```

### Database Location
- **Development**: `../data/new-api.db` (project directory)
- **Production**:
  - macOS: `~/Library/Application Support/New API/data/`
  - Windows: `%APPDATA%/New API/data/`
  - Linux: `~/.config/New API/data/`
