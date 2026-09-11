# S-Pankki Sandbox

A comprehensive sandbox implementation for S-Pankki Open Banking APIs. This project includes both a Go-based backend server and an iOS client application for testing and development.

## Overview

S-Pankki Sandbox provides a complete environment for developing and testing applications that integrate with S-Pankki's Open Banking APIs. It includes:

- **Backend Server**: Handles OAuth2/OIDC authentication flows, certificate management, and API integration
- **iOS Client**: Native iOS application for account and payment operations
- **OpenBanking API Support**: Account Information Service Provider (AISP) and Payment Initiation Service Provider (PISP) implementations

## Architecture

### System Design

```
┌─────────────────────────────────────────────────────────────┐
│                    iOS Client Application                    │
│                  (Swift/SwiftUI Frontend)                    │
└─────────────────────────────┬───────────────────────────────┘
                              │
                     REST API (HTTPS/mTLS)
                              │
┌─────────────────────────────▼───────────────────────────────┐
│               S-Pankki Sandbox Backend Server                │
│                      (Go, Port 8080)                         │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  OAuth2/OIDC Hybrid Flow Handler                      │   │
│  │  - Authorization endpoint                             │   │
│  │  - Token exchange                                     │   │
│  │  - Refresh token management                           │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Certificate Management & mTLS                        │   │
│  │  - TLS client certificate handling                    │   │
│  │  - Digital signature certificates                     │   │
│  │  - JWT signing & verification                         │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  API Handlers                                         │   │
│  │  - Account Information (AISP)                         │   │
│  │  - Payment Initiation (PISP)                          │   │
│  │  - Transaction queries                                │   │
│  │  - Payment status tracking                            │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                               │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  Web UI Dashboard                                     │   │
│  │  - Account and transaction browsing                   │   │
│  │  - Call log inspection                                │   │
│  │  - OAuth status tracking                              │   │
│  └──────────────────────────────────────────────────────┘   │
└─────────────────────────────┬───────────────────────────────┘
                              │
                     mTLS with Signing
                              │
┌─────────────────────────────▼───────────────────────────────┐
│            S-Pankki Open Banking API Server                  │
│     (Account & Transaction v3.1.6, Payment Init v3.1.7)     │
└─────────────────────────────────────────────────────────────┘
```

### Backend Components

#### 1. **Configuration (config.go)**
- Environment-based configuration management
- Certificate paths and credentials loading
- Support for `.env` file configuration

#### 2. **Banking Engine (bank.go)**
- Core banking operations
- OAuth2/OIDC flow coordination
- API client management
- Account and transaction data retrieval

#### 3. **Payment Processing (payments.go)**
- Payment submission and tracking
- Payment Initiation Service Provider (PISP) operations
- Payment status management

#### 4. **HTTP Handlers (handlers.go)**
- RESTful endpoint implementations
- Request validation and response formatting
- Error handling and logging

#### 5. **Call Logging (calllog.go)**
- API request/response logging
- Audit trail maintenance
- Debugging support

#### 6. **Web Dashboard (web/)**
- HTML/CSS/JavaScript interface
- Account browsing
- Transaction filtering and viewing
- OAuth session management UI

### iOS Client Components

#### **Core Application**
- **SpankkiSandboxApp**: Application entry point with SwiftUI scene setup
- **ContentView**: Main UI presenting account and payment information
- **AppModel**: State management and data coordination

#### **Networking**
- **ServerClient**: HTTP client for backend communication with OAuth token handling

#### **Data Models**
- **Models**: Swift data structures matching OpenBanking API responses

#### **Authentication**
- **AuthWebView**: OAuth2/OIDC authentication flow via WebView

## API Specifications

### Supported APIs

1. **Account Information Service Provider (AISP) v3.1.6**
   - Account access and list
   - Account details retrieval
   - Balance information
   - Transaction history

2. **Payment Initiation Service Provider (PISP) v3.1.7**
   - Payment submission
   - Payment status tracking
   - Funds confirmation

## Setup and Configuration

### Backend Server

#### Prerequisites
- Go 1.16 or later
- S-Pankki sandbox credentials (Client ID and API Key)
- TLS certificates (client and signing certificates)

#### Configuration

1. Copy `.env.example` to `.env`:
```bash
cp server/.env.example server/.env
```

2. Fill in required environment variables:
```
SPANKKI_CLIENT_ID=your_client_id
SPANKKI_API_KEY=your_api_key
```

3. Optional overrides:
```
LISTEN_ADDR=:8080
SPANKKI_REDIRECT_URI=http://localhost:8080/callback
SPANKKI_TLS_CERT=certs/tls.crt
SPANKKI_TLS_KEY=certs/tls.key
SPANKKI_SIGNING_CERT=certs/signing.crt
SPANKKI_SIGNING_KEY=certs/signing.key
```

#### Running the Server

```bash
cd server
go run .
```

The server will:
- Listen on `http://localhost:8080` (default)
- Serve the web dashboard at `http://localhost:8080/`
- Provide API endpoints at `/api/*`

### iOS Client

#### Prerequisites
- Xcode 14.0 or later
- iOS 17.0 or later deployment target
- Development team configured for signing

#### Running the App

1. Open `SpankkiSandbox.xcodeproj` in Xcode
2. Select the `SpankkiSandbox` target
3. Configure your development team in Build Settings
4. Build and run on a simulator or device

## API Endpoints

### Authentication
- `GET /auth/authorize` - Initiate OAuth2/OIDC flow
- `GET /callback` - OAuth2/OIDC callback handler
- `POST /api/refresh` - Refresh access token

### Accounts
- `GET /api/status` - Get current session and connection status
- `GET /api/accounts` - List all authorized accounts
- `GET /api/accounts/{id}` - Get account details

### Transactions
- `GET /api/transactions` - Get transactions for selected account
- `GET /api/transactions?from={date}&to={date}` - Filter transactions by date range

### Payments
- `POST /api/payments` - Initiate a payment
- `GET /api/payments` - List payments
- `GET /api/payments/{id}` - Get payment status

### Debugging
- `GET /api/calllog` - View API call history

## Development

### Code Generation

The backend uses OpenAPI code generation for type-safe API client integration:

```bash
cd server/internal/spankki
oapi-codegen -config oapi-codegen.yaml ../../Account\ and\ Transaction\ API\ v3.1.6.yml
```

### Testing

The web dashboard provides a built-in testing interface for OAuth flows and API operations.

## Security Considerations

- **mTLS**: All communication with S-Pankki API uses mutual TLS
- **JWT Signing**: Requests include digitally signed JWTs for non-repudiation
- **Certificate Management**: Keys are stored in the `certs/` directory and should be kept secure
- **Environment Secrets**: Credentials should never be committed to version control

## License

Proprietary - S-Pankki Sandbox Implementation
