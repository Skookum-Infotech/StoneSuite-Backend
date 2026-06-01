-- Users
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY,
    email VARCHAR(255) UNIQUE NOT NULL,
    password_hash TEXT,
    full_name VARCHAR(255) NOT NULL,
    oauth_provider VARCHAR(50),
    oauth_id TEXT,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    failed_login_attempts INT NOT NULL DEFAULT 0,
    is_locked BOOLEAN NOT NULL DEFAULT FALSE,
    locked_until TIMESTAMPTZ,
    password_reset_token TEXT,
    password_reset_expiry TIMESTAMPTZ,
    email_verification_code TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users(LOWER(email));

-- Customers
CREATE TABLE IF NOT EXISTS customers (
    id UUID PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    legal_name VARCHAR(255),
    industry VARCHAR(255),
    website VARCHAR(255),
    country VARCHAR(100),
    currency VARCHAR(10),
    timezone VARCHAR(100),
    tax_id VARCHAR(100),
    billing_address TEXT,
    shipping_address TEXT,
    return_address TEXT,
    status VARCHAR(50) NOT NULL DEFAULT 'pendingApproval',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customers_status ON customers(status);

-- Customer contacts
CREATE TABLE IF NOT EXISTS customer_contacts (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    full_name VARCHAR(255) NOT NULL,
    email VARCHAR(255) NOT NULL,
    phone VARCHAR(50),
    role VARCHAR(100) NOT NULL DEFAULT 'super_admin',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_customer_contacts_customer ON customer_contacts(customer_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_customer_contact_unique_email ON customer_contacts(customer_id, LOWER(email));

-- Onboarding invites
CREATE TABLE IF NOT EXISTS onboarding_invites (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    contact_id UUID REFERENCES customer_contacts(id) ON DELETE SET NULL,
    contact_email VARCHAR(255) NOT NULL,
    token VARCHAR(128) NOT NULL UNIQUE,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    expires_at TIMESTAMPTZ NOT NULL,
    sent_at TIMESTAMPTZ,
    accepted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_onboarding_invites_customer ON onboarding_invites(customer_id);
CREATE INDEX IF NOT EXISTS idx_onboarding_invites_token ON onboarding_invites(token);

-- Onboarding audit logs
CREATE TABLE IF NOT EXISTS onboarding_audit_logs (
    id UUID PRIMARY KEY,
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    invite_id UUID REFERENCES onboarding_invites(id) ON DELETE SET NULL,
    actor_id UUID,
    actor_email VARCHAR(255),
    action VARCHAR(100) NOT NULL,
    details TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_onboarding_audit_customer ON onboarding_audit_logs(customer_id);

-- CRM leads
CREATE TABLE IF NOT EXISTS leads (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    lead_id VARCHAR(50) UNIQUE NOT NULL,
    custom_form VARCHAR(255) NOT NULL DEFAULT 'Standard Lead Form',
    lead_status VARCHAR(100) NOT NULL DEFAULT 'LEAD-Unqualified',
    default_order_priority VARCHAR(100),
    type VARCHAR(50) NOT NULL DEFAULT 'Company',
    company_name VARCHAR(255),
    first_name VARCHAR(255),
    last_name VARCHAR(255),
    sales_rep VARCHAR(255),
    territory VARCHAR(255),
    partner VARCHAR(255),
    email VARCHAR(255),
    phone VARCHAR(100),
    fax VARCHAR(100),
    address TEXT,
    primary_subsidiary VARCHAR(255),
    email_for_payment_notification VARCHAR(255),
    white_glove BOOLEAN NOT NULL DEFAULT FALSE,
    display_product_code BOOLEAN NOT NULL DEFAULT FALSE,
    blackline_ar_cash_app BOOLEAN NOT NULL DEFAULT FALSE,
    sfdc_account_id VARCHAR(255),
    prev_external_id VARCHAR(255),
    sfdc_customer_status VARCHAR(255),
    crm_account_owner VARCHAR(255),
    customer_legal_name VARCHAR(255),
    customer_type VARCHAR(100) DEFAULT 'Customer',
    crm_csm_team VARCHAR(255),
    sfdc_external_id VARCHAR(255),
    additional_emails TEXT,
    crm_csm VARCHAR(255),
    talkdesk_region VARCHAR(255),
    crm_growth_manager VARCHAR(255),
    talkdesk_id_platform VARCHAR(255),
    zuora_invoice_name VARCHAR(255),
    estimated_budget VARCHAR(100),
    budget_approved BOOLEAN NOT NULL DEFAULT FALSE,
    sales_readiness VARCHAR(100),
    buying_reason VARCHAR(255),
    buying_time_frame VARCHAR(100),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_leads_status ON leads(lead_status);
CREATE INDEX IF NOT EXISTS idx_leads_type ON leads(type);
