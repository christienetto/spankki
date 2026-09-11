# S-Pankki Sandbox - Architecture Details

## High-Level Design Goals

1. **Compliance**: Implement OpenBanking API standards (PSD2, OAuth2, OIDC)
2. **Security**: Use mTLS and digital signatures for all API communication
3. **Developer Experience**: Provide both programmatic (REST) and visual (web UI) interfaces
4. **Separation of Concerns**: Isolate authentication, banking logic, and API integration

## Backend Architecture

### Request Flow

```
Client Request
    ↓
HTTP Router (mux)
    ↓
Authentication Middleware
    ↓
Handler (handlers.go)
    ├─→ Validate Request
    ├─→ Access Banking State (bank.go)
    ├─→ Call S-Pankki API (via generated client)
    ├─→ Transform Response
    └─→ Return JSON
    ↓
Logging (calllog.go)
```

### Component Responsibilities

#### **bank.go - Banking Engine**
The core orchestrator for all banking operations.

- **Initialization**: Loads certificates, initializes OAuth client
- **OAuth2 Flow Management**: Handles authorization, token exchange, token refresh
- **API Client Wrapper**: Provides convenient methods to call S-Pankki APIs
- **State Management**: Maintains access tokens, client configuration, and session data

Key types:
```go
type bank struct {
    cfg    config
    client *http.Client           // mTLS client
    oidcCfg *oidc.Config           // OAuth2/OIDC configuration
    accessToken string             // Current access token
    // ...
}
```

#### **handlers.go - HTTP API Layer**
Maps incoming HTTP requests to banking operations.

Implemented handlers:
- `statusHandler()` - Returns current session state, connection status
- `accountsHandler()` - Lists all authorized accounts
- `accountDetailsHandler()` - Gets balance and info for specific account
- `transactionsHandler()` - Retrieves transaction history (with date filtering)
- `paymentsHandler()` - CRUD operations for payments
- `authHandler()` - Initiates OAuth flow
- `callbackHandler()` - Processes OAuth callback
- `refreshHandler()` - Refreshes expired tokens
- `calllogHandler()` - Returns API call history for debugging

#### **payments.go - Payment Service**
Specializes in payment operations.

- **Payment Submission**: Formats and submits payments to S-Pankki API
- **Status Tracking**: Polls payment status from the bank
- **Error Handling**: Manages payment-specific error scenarios

Key operations:
- Validate payment details (amount, currency, recipient)
- Format payment request per PISP spec
- Handle payment rejection/confirmation responses

#### **calllog.go - Request Logging**
Maintains an audit trail of all API interactions.

- Captures request details (method, URL, headers, body)
- Records response status and body
- Supports filtering and searching logs
- Persisted in memory (lost on server restart)

#### **config.go - Configuration**
Handles environment-based setup.

- Reads from `.env` file or environment variables
- Validates required credentials (Client ID, API Key)
- Provides sensible defaults
- Supports certificate path overrides

### Data Flow: OAuth2 OIDC Hybrid Flow

```
1. User clicks "Connect" in iOS app or web dashboard
2. app → backend: GET /auth/authorize
3. backend → S-Pankki: Redirects user to OIDC authorization endpoint
4. User logs into S-Pankki and grants consent
5. S-Pankki → backend: Redirects to /callback with authorization code
6. backend → S-Pankki: POST token endpoint with authorization code + mTLS cert
7. S-Pankki → backend: Returns access token (+ ID token, refresh token)
8. backend → app: Returns user to client with session established
9. app ↔ backend: All subsequent API calls use access token
```

### Data Flow: Account Access

```
1. iOS app: GET /api/accounts
2. backend → S-Pankki: GET /accounts (with Bearer token + JWT signature)
3. S-Pankki → backend: Returns account list (JSON per spec)
4. backend: Parses and filters response
5. backend → iOS app: Returns simplified JSON
```

### Certificate Management

Three types of certificates are managed:

1. **TLS Client Certificate** (mutual TLS)
   - Used to authenticate the backend server to S-Pankki
   - Loaded in `bank.go` during initialization
   - Configured via `SPANKKI_TLS_CERT` and `SPANKKI_TLS_KEY`

2. **Signing Certificate** (JWT/JWS)
   - Used to digitally sign request JWTs
   - Extracted public key ID (KID) and issuer from certificate
   - Can be overridden via `SPANKKI_SIGNING_KID` and `SPANKKI_SIGNING_ISSUER`
   - Loaded in `bank.go`

3. **API Keys**
   - Client ID: Identifies the TPP (Third Party Provider)
   - API Key: Secret credential for server-to-server operations

### iOS Client Architecture

#### **Data Flow**

```
User Action (iOS UI)
    ↓
ContentView (SwiftUI)
    ↓
AppModel (MVVM State)
    ├─→ Updates @Published properties
    └─→ Calls ServerClient methods
    ↓
ServerClient (HTTP Client)
    ├─→ Constructs request
    ├─→ Includes OAuth token
    └─→ Sends to backend
    ↓
Backend (processes and returns data)
    ↓
ServerClient (parses response)
    ↓
AppModel (updates state)
    ↓
ContentView (refreshes UI)
```

#### **MVVM Architecture**

- **Model**: Swift data structures (`Models.swift`) matching API responses
- **ViewModel**: `AppModel` manages app state and handles business logic
- **View**: `ContentView` displays data and handles user interactions

#### **Key Components**

1. **AppModel**: State container
   - Holds accounts, transactions, payments
   - Manages loading states and errors
   - Coordinates API calls via ServerClient

2. **ServerClient**: Networking layer
   - Constructs HTTP requests
   - Handles OAuth token management
   - Deserializes JSON responses
   - Manages errors

3. **ContentView**: Main UI
   - Displays account list
   - Shows transaction history
   - Provides payment submission interface
   - Handles authentication UI flow

4. **AuthWebView**: OAuth handling
   - Presents WebView for user authentication
   - Captures OAuth callback
   - Returns authenticated session to app

### Web Dashboard Architecture

The web dashboard (`server/web/`) provides a rich client-side interface for testing.

#### **Technologies**
- **HTML/CSS**: Structure and styling
- **Vanilla JavaScript**: No framework dependencies (lightweight)
- **Fetch API**: Client-server communication

#### **Key Features**
- Account browsing with balance display
- Transaction filtering (by date range, type)
- Payment submission and tracking
- OAuth connection status indicator
- API call log viewer with JSON formatting
- Token expiration timer with refresh UI

#### **UI State Management**
All state is kept in a single JavaScript object:
```javascript
const state = {
  status: null,        // Session status
  accounts: [],        // Account list
  selectedId: null,    // Selected account
  tx: [],              // Transactions
  type: "all",         // Transaction filter
  payments: [],        // Payment list
  log: [],             // API call log
  // ...
};
```

Updates are driven by API responses, and the DOM is re-rendered reactively.

## Error Handling

### Backend
- HTTP status codes match REST conventions
- Error responses include `error` field with message
- Validation errors return 400 Bad Request
- Authentication errors return 401 Unauthorized
- API integration errors return 502 Bad Gateway (with wrapped error)

### iOS Client
- NetworkError types distinguish connection, HTTP, and parsing errors
- User-visible error messages via alert dialogs
- Graceful degradation (partial data display on partial failures)

### Web Dashboard
- Toast notifications for errors and success messages
- Call log inspection for debugging API issues
- Status indicator shows connectivity state

## Performance Considerations

1. **Token Caching**: Access tokens are cached in memory, reducing authentication overhead
2. **Transaction Pagination**: Large transaction lists use pagination via API cursors
3. **Call Logging**: Limited in-memory log to prevent unbounded growth
4. **Client-Side Rendering**: Web dashboard uses client-side rendering for responsiveness

## Security Measures

### Transport Security
- **HTTPS**: Required for all production deployments
- **mTLS**: Backend authenticates to S-Pankki using client certificates
- **TLS 1.2+**: Enforced via Go's crypto/tls defaults

### Data Protection
- **JWT Signing**: Requests include cryptographic signatures preventing tampering
- **Token Validation**: Access tokens validated on each request
- **Secrets Management**: Credentials via environment variables, never hardcoded

### API Security
- **OIDC/OAuth2**: Industry-standard authentication
- **Consent Flow**: User explicitly authorizes data access
- **Scoped Access**: Backend only accesses data within consent scope
- **Token Expiration**: Automatic token refresh for expired tokens

## Testing Strategy

### Manual Testing
1. Use the web dashboard for interactive testing
2. Monitor API calls in the call log
3. Verify token refresh behavior with token expiration
4. Test error cases (invalid payments, etc.)

### Integration Testing
- Backend integrates with real S-Pankki sandbox API
- No mocking of external services
- End-to-end flow testing via web dashboard and iOS app

## Deployment

### Development
- Run server locally: `go run .` in `server/` directory
- Run iOS app in Xcode simulator or device
- Configure `.env` with sandbox credentials

### Production Considerations
- Use environment-based secrets management (not `.env` files)
- Enable HTTPS with valid certificates
- Use production S-Pankki endpoints (not sandbox)
- Implement rate limiting and request queuing
- Monitor certificate expiration
- Set up logging and alerting for API errors
- Consider horizontal scaling with session persistence

## Future Enhancements

1. **Database Persistence**: Replace in-memory state with persistent storage
2. **Multi-User Support**: Add per-user session management
3. **Enhanced Dashboard**: Real-time updates via WebSockets
4. **Mobile-First Design**: Improve responsive design for mobile web access
5. **Advanced Filtering**: More sophisticated transaction search and categorization
6. **Payment Scheduling**: Support for scheduled payments
7. **Webhook Integration**: Receive payment status updates asynchronously
