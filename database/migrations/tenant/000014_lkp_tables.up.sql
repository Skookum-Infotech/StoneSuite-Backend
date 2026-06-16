-- =====================================================================
-- Tenant migration 013: ERP lookup tables (v2 relational design).
-- Source: StoneSuite_Lookup_DDL_DML_v1.sql (Elevation Stone). Converted to
-- idempotent form: CREATE TABLE IF NOT EXISTS with inline constraints, and
-- INSERT ... ON CONFLICT DO NOTHING so the runner can replay safely.
-- All audit columns reference employee(employee_id); seed rows use id 1.
-- =====================================================================

-- 1. lkp_currency -----------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_currency (
    currency_id             SERIAL       PRIMARY KEY,
    currency_name           VARCHAR(50)  NOT NULL,
    currency_code           VARCHAR(5)   NOT NULL,
    currency_symbol         VARCHAR(5)   NOT NULL,
    currency_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    currency_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    currency_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    currency_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    currency_deleted_at     TIMESTAMP        NULL,
    currency_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    currency_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_currency_code UNIQUE (currency_code)
);

INSERT INTO lkp_currency (currency_name, currency_code, currency_symbol, currency_is_active, currency_is_system, currency_created_by) VALUES
    ('US Dollar',          'USD',  '$',  TRUE, TRUE, 1),
    ('Canadian Dollar',    'CAD',  'C$', TRUE, TRUE, 1),
    ('Mexican Peso',       'MXN',  '$',  TRUE, TRUE, 1),
    ('Indian Rupee',       'INR',  '₹',  TRUE, TRUE, 1),
    ('Euro',               'EUR',  '€',  TRUE, TRUE, 1),
    ('British Pound',      'GBP',  '£',  TRUE, TRUE, 1),
    ('Australian Dollar',  'AUD',  'A$', TRUE, TRUE, 1),
    ('UAE Dirham',         'AED',  'د.إ',TRUE, TRUE, 1)
ON CONFLICT (currency_code) DO NOTHING;

-- 2. lkp_country ------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_country (
    country_id                  SERIAL       PRIMARY KEY,
    country_name                VARCHAR(50)  NOT NULL,
    country_code2               VARCHAR(5)   NOT NULL,
    country_code3               VARCHAR(5)   NOT NULL,
    country_locale              VARCHAR(10)      NULL,
    country_phone_code          VARCHAR(10)  NOT NULL,
    country_default_currency_id INTEGER      NOT NULL REFERENCES lkp_currency(currency_id),
    country_is_active           BOOLEAN      NOT NULL DEFAULT TRUE,
    country_is_system           BOOLEAN      NOT NULL DEFAULT FALSE,
    country_created_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    country_created_by          INTEGER      NOT NULL REFERENCES employee(employee_id),
    country_deleted_at          TIMESTAMP        NULL,
    country_deleted_by          INTEGER          NULL REFERENCES employee(employee_id),
    country_record_version      INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_country_code2 UNIQUE (country_code2),
    CONSTRAINT uq_country_code3 UNIQUE (country_code3)
);

INSERT INTO lkp_country (country_name, country_code2, country_code3, country_locale, country_phone_code, country_default_currency_id, country_is_active, country_is_system, country_created_by) VALUES
    ('United States of America', 'US', 'USA', 'en-US', '+1',   1, TRUE, TRUE, 1),
    ('Canada',                   'CA', 'CAN', 'en-CA', '+1',   2, TRUE, TRUE, 1),
    ('Mexico',                   'MX', 'MEX', 'es-MX', '+52',  3, TRUE, TRUE, 1),
    ('India',                    'IN', 'IND', 'en-IN', '+91',  4, TRUE, TRUE, 1)
ON CONFLICT (country_code2) DO NOTHING;

-- 3. lkp_state --------------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_state (
    state_id            SERIAL       PRIMARY KEY,
    state_country_id    INTEGER      NOT NULL REFERENCES lkp_country(country_id),
    state_name          VARCHAR(50)  NOT NULL,
    state_code          VARCHAR(5)   NOT NULL,
    state_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    state_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    state_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    state_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    state_deleted_at    TIMESTAMP        NULL,
    state_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    state_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_state_code_country UNIQUE (state_country_id, state_code)
);

INSERT INTO lkp_state (state_country_id, state_name, state_code, state_is_active, state_is_system, state_created_by) VALUES
    (1, 'Alabama', 'AL', TRUE, TRUE, 1), (1, 'Alaska', 'AK', TRUE, TRUE, 1), (1, 'Arizona', 'AZ', TRUE, TRUE, 1),
    (1, 'Arkansas', 'AR', TRUE, TRUE, 1), (1, 'California', 'CA', TRUE, TRUE, 1), (1, 'Colorado', 'CO', TRUE, TRUE, 1),
    (1, 'Connecticut', 'CT', TRUE, TRUE, 1), (1, 'Delaware', 'DE', TRUE, TRUE, 1), (1, 'Florida', 'FL', TRUE, TRUE, 1),
    (1, 'Georgia', 'GA', TRUE, TRUE, 1), (1, 'Hawaii', 'HI', TRUE, TRUE, 1), (1, 'Idaho', 'ID', TRUE, TRUE, 1),
    (1, 'Illinois', 'IL', TRUE, TRUE, 1), (1, 'Indiana', 'IN', TRUE, TRUE, 1), (1, 'Iowa', 'IA', TRUE, TRUE, 1),
    (1, 'Kansas', 'KS', TRUE, TRUE, 1), (1, 'Kentucky', 'KY', TRUE, TRUE, 1), (1, 'Louisiana', 'LA', TRUE, TRUE, 1),
    (1, 'Maine', 'ME', TRUE, TRUE, 1), (1, 'Maryland', 'MD', TRUE, TRUE, 1), (1, 'Massachusetts', 'MA', TRUE, TRUE, 1),
    (1, 'Michigan', 'MI', TRUE, TRUE, 1), (1, 'Minnesota', 'MN', TRUE, TRUE, 1), (1, 'Mississippi', 'MS', TRUE, TRUE, 1),
    (1, 'Missouri', 'MO', TRUE, TRUE, 1), (1, 'Montana', 'MT', TRUE, TRUE, 1), (1, 'Nebraska', 'NE', TRUE, TRUE, 1),
    (1, 'Nevada', 'NV', TRUE, TRUE, 1), (1, 'New Hampshire', 'NH', TRUE, TRUE, 1), (1, 'New Jersey', 'NJ', TRUE, TRUE, 1),
    (1, 'New Mexico', 'NM', TRUE, TRUE, 1), (1, 'New York', 'NY', TRUE, TRUE, 1), (1, 'North Carolina', 'NC', TRUE, TRUE, 1),
    (1, 'North Dakota', 'ND', TRUE, TRUE, 1), (1, 'Ohio', 'OH', TRUE, TRUE, 1), (1, 'Oklahoma', 'OK', TRUE, TRUE, 1),
    (1, 'Oregon', 'OR', TRUE, TRUE, 1), (1, 'Pennsylvania', 'PA', TRUE, TRUE, 1), (1, 'Rhode Island', 'RI', TRUE, TRUE, 1),
    (1, 'South Carolina', 'SC', TRUE, TRUE, 1), (1, 'South Dakota', 'SD', TRUE, TRUE, 1), (1, 'Tennessee', 'TN', TRUE, TRUE, 1),
    (1, 'Texas', 'TX', TRUE, TRUE, 1), (1, 'Utah', 'UT', TRUE, TRUE, 1), (1, 'Vermont', 'VT', TRUE, TRUE, 1),
    (1, 'Virginia', 'VA', TRUE, TRUE, 1), (1, 'Washington', 'WA', TRUE, TRUE, 1), (1, 'West Virginia', 'WV', TRUE, TRUE, 1),
    (1, 'Wisconsin', 'WI', TRUE, TRUE, 1), (1, 'Wyoming', 'WY', TRUE, TRUE, 1), (1, 'District of Columbia', 'DC', TRUE, TRUE, 1),
    (2, 'Alberta', 'AB', TRUE, TRUE, 1), (2, 'British Columbia', 'BC', TRUE, TRUE, 1), (2, 'Manitoba', 'MB', TRUE, TRUE, 1),
    (2, 'New Brunswick', 'NB', TRUE, TRUE, 1), (2, 'Newfoundland and Labrador', 'NL', TRUE, TRUE, 1), (2, 'Nova Scotia', 'NS', TRUE, TRUE, 1),
    (2, 'Ontario', 'ON', TRUE, TRUE, 1), (2, 'Prince Edward Island', 'PE', TRUE, TRUE, 1), (2, 'Quebec', 'QC', TRUE, TRUE, 1),
    (2, 'Saskatchewan', 'SK', TRUE, TRUE, 1), (2, 'Northwest Territories', 'NT', TRUE, TRUE, 1), (2, 'Nunavut', 'NU', TRUE, TRUE, 1),
    (2, 'Yukon', 'YT', TRUE, TRUE, 1),
    (3, 'Aguascalientes', 'AG', TRUE, TRUE, 1), (3, 'Baja California', 'BC', TRUE, TRUE, 1), (3, 'Baja California Sur', 'BS', TRUE, TRUE, 1),
    (3, 'Campeche', 'CM', TRUE, TRUE, 1), (3, 'Chiapas', 'CS', TRUE, TRUE, 1), (3, 'Chihuahua', 'CH', TRUE, TRUE, 1),
    (3, 'Ciudad de Mexico', 'CX', TRUE, TRUE, 1), (3, 'Coahuila', 'CO', TRUE, TRUE, 1), (3, 'Colima', 'CL', TRUE, TRUE, 1),
    (3, 'Durango', 'DG', TRUE, TRUE, 1), (3, 'Guanajuato', 'GT', TRUE, TRUE, 1), (3, 'Guerrero', 'GR', TRUE, TRUE, 1),
    (3, 'Hidalgo', 'HG', TRUE, TRUE, 1), (3, 'Jalisco', 'JA', TRUE, TRUE, 1), (3, 'Mexico State', 'EM', TRUE, TRUE, 1),
    (3, 'Michoacan', 'MI', TRUE, TRUE, 1), (3, 'Morelos', 'MO', TRUE, TRUE, 1), (3, 'Nayarit', 'NA', TRUE, TRUE, 1),
    (3, 'Nuevo Leon', 'NL', TRUE, TRUE, 1), (3, 'Oaxaca', 'OA', TRUE, TRUE, 1), (3, 'Puebla', 'PU', TRUE, TRUE, 1),
    (3, 'Queretaro', 'QT', TRUE, TRUE, 1), (3, 'Quintana Roo', 'QR', TRUE, TRUE, 1), (3, 'San Luis Potosi', 'SL', TRUE, TRUE, 1),
    (3, 'Sinaloa', 'SI', TRUE, TRUE, 1), (3, 'Sonora', 'SO', TRUE, TRUE, 1), (3, 'Tabasco', 'TB', TRUE, TRUE, 1),
    (3, 'Tamaulipas', 'TM', TRUE, TRUE, 1), (3, 'Tlaxcala', 'TL', TRUE, TRUE, 1), (3, 'Veracruz', 'VE', TRUE, TRUE, 1),
    (3, 'Yucatan', 'YU', TRUE, TRUE, 1), (3, 'Zacatecas', 'ZA', TRUE, TRUE, 1),
    (4, 'Andhra Pradesh', 'AP', TRUE, TRUE, 1), (4, 'Arunachal Pradesh', 'AR', TRUE, TRUE, 1), (4, 'Assam', 'AS', TRUE, TRUE, 1),
    (4, 'Bihar', 'BR', TRUE, TRUE, 1), (4, 'Chhattisgarh', 'CG', TRUE, TRUE, 1), (4, 'Goa', 'GA', TRUE, TRUE, 1),
    (4, 'Gujarat', 'GJ', TRUE, TRUE, 1), (4, 'Haryana', 'HR', TRUE, TRUE, 1), (4, 'Himachal Pradesh', 'HP', TRUE, TRUE, 1),
    (4, 'Jharkhand', 'JH', TRUE, TRUE, 1), (4, 'Karnataka', 'KA', TRUE, TRUE, 1), (4, 'Kerala', 'KL', TRUE, TRUE, 1),
    (4, 'Madhya Pradesh', 'MP', TRUE, TRUE, 1), (4, 'Maharashtra', 'MH', TRUE, TRUE, 1), (4, 'Manipur', 'MN', TRUE, TRUE, 1),
    (4, 'Meghalaya', 'ML', TRUE, TRUE, 1), (4, 'Mizoram', 'MZ', TRUE, TRUE, 1), (4, 'Nagaland', 'NL', TRUE, TRUE, 1),
    (4, 'Odisha', 'OD', TRUE, TRUE, 1), (4, 'Punjab', 'PB', TRUE, TRUE, 1), (4, 'Rajasthan', 'RJ', TRUE, TRUE, 1),
    (4, 'Sikkim', 'SK', TRUE, TRUE, 1), (4, 'Tamil Nadu', 'TN', TRUE, TRUE, 1), (4, 'Telangana', 'TG', TRUE, TRUE, 1),
    (4, 'Tripura', 'TR', TRUE, TRUE, 1), (4, 'Uttar Pradesh', 'UP', TRUE, TRUE, 1), (4, 'Uttarakhand', 'UK', TRUE, TRUE, 1),
    (4, 'West Bengal', 'WB', TRUE, TRUE, 1), (4, 'Andaman and Nicobar Islands', 'AN', TRUE, TRUE, 1), (4, 'Chandigarh', 'CH', TRUE, TRUE, 1),
    (4, 'Dadra and Nagar Haveli and Daman and Diu', 'DD', TRUE, TRUE, 1), (4, 'Delhi', 'DL', TRUE, TRUE, 1), (4, 'Jammu and Kashmir', 'JK', TRUE, TRUE, 1),
    (4, 'Ladakh', 'LA', TRUE, TRUE, 1), (4, 'Lakshadweep', 'LD', TRUE, TRUE, 1), (4, 'Puducherry', 'PY', TRUE, TRUE, 1)
ON CONFLICT (state_country_id, state_code) DO NOTHING;

-- 4. lkp_record_type --------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_record_type (
    record_type_id          SERIAL       PRIMARY KEY,
    record_type_code        VARCHAR(10)  NOT NULL,
    record_type_code_full   VARCHAR(50)  NOT NULL,
    record_type_name        VARCHAR(50)  NOT NULL,
    record_type_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    record_type_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    record_type_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    record_type_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    record_type_deleted_at  TIMESTAMP        NULL,
    record_type_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    record_type_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_record_type_code UNIQUE (record_type_code)
);

INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('LEAD', 'lead',             'Lead',             TRUE, TRUE, 1),
    ('PROS', 'prospect',         'Prospect',         TRUE, TRUE, 1),
    ('CUST', 'customer',         'Customer',         TRUE, TRUE, 1),
    ('ESTM', 'estimate',         'Estimate',         TRUE, TRUE, 1),
    ('QUOT', 'quote',            'Quote',            TRUE, TRUE, 1),
    ('SORD', 'salesorder',       'Sales Order',      TRUE, TRUE, 1),
    ('INVC', 'invoice',          'Invoice',          TRUE, TRUE, 1),
    ('PYMT', 'payment',          'Payment',          TRUE, TRUE, 1),
    ('CRDT', 'creditmemo',       'Credit Memo',      TRUE, TRUE, 1),
    ('RFND', 'customerrefund',   'Customer Refund',  TRUE, TRUE, 1),
    ('VNDR', 'vendor',           'Vendor',           TRUE, TRUE, 1),
    ('REQN', 'requisition',      'Requisition',      TRUE, TRUE, 1),
    ('PORD', 'purchaseorder',    'Purchase Order',   TRUE, TRUE, 1),
    ('IRCT', 'itemreceipt',      'Item Receipt',     TRUE, TRUE, 1),
    ('VBIL', 'vendorbill',       'Vendor Bill',      TRUE, TRUE, 1),
    ('VPAY', 'vendorpayment',    'Vendor Payment',   TRUE, TRUE, 1),
    ('VCRD', 'vendorcredit',     'Vendor Credit',    TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

-- 5. lkp_record_status ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_record_status (
    record_status_id            SERIAL       PRIMARY KEY,
    record_status_code          VARCHAR(10)  NOT NULL,
    record_status_name          VARCHAR(50)  NOT NULL,
    record_status_record_type   INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    record_status_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    record_status_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    record_status_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    record_status_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    record_status_deleted_at    TIMESTAMP        NULL,
    record_status_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    record_status_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_record_status_code_type UNIQUE (record_status_code, record_status_record_type)
);

INSERT INTO lkp_record_status (record_status_code, record_status_name, record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by) VALUES
    ('ACT_', 'Active', 1, TRUE, TRUE, 1), ('INA_', 'Inactive', 1, TRUE, TRUE, 1), ('CANC', 'Cancelled', 1, TRUE, TRUE, 1),
    ('ACT_', 'Active', 2, TRUE, TRUE, 1), ('INA_', 'Inactive', 2, TRUE, TRUE, 1), ('CANC', 'Cancelled', 2, TRUE, TRUE, 1),
    ('ACT_', 'Active', 3, TRUE, TRUE, 1), ('INA_', 'Inactive', 3, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 4, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 4, TRUE, TRUE, 1), ('APPV', 'Approved', 4, TRUE, TRUE, 1),
    ('SENT', 'Sent', 4, TRUE, TRUE, 1), ('CANC', 'Cancelled', 4, TRUE, TRUE, 1), ('RJCT', 'Rejected', 4, TRUE, TRUE, 1), ('EXPR', 'Expired', 4, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 5, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 5, TRUE, TRUE, 1), ('APPV', 'Approved', 5, TRUE, TRUE, 1),
    ('SENT', 'Sent', 5, TRUE, TRUE, 1), ('CANC', 'Cancelled', 5, TRUE, TRUE, 1), ('RJCT', 'Rejected', 5, TRUE, TRUE, 1), ('EXPR', 'Expired', 5, TRUE, TRUE, 1), ('CONV', 'Converted', 5, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 6, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 6, TRUE, TRUE, 1), ('APPV', 'Approved', 6, TRUE, TRUE, 1),
    ('OPEN', 'Open', 6, TRUE, TRUE, 1), ('PART', 'Partially Filled', 6, TRUE, TRUE, 1), ('FILL', 'Filled', 6, TRUE, TRUE, 1), ('CANC', 'Cancelled', 6, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 7, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 7, TRUE, TRUE, 1), ('APPV', 'Approved', 7, TRUE, TRUE, 1),
    ('SENT', 'Sent', 7, TRUE, TRUE, 1), ('PART', 'Partially Paid', 7, TRUE, TRUE, 1), ('PAID', 'Paid', 7, TRUE, TRUE, 1), ('ODUE', 'Overdue', 7, TRUE, TRUE, 1), ('VOID', 'Void', 7, TRUE, TRUE, 1),
    ('PEND', 'Pending', 8, TRUE, TRUE, 1), ('APPV', 'Approved', 8, TRUE, TRUE, 1), ('DEPO', 'Deposited', 8, TRUE, TRUE, 1), ('VOID', 'Void', 8, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 9, TRUE, TRUE, 1), ('APPV', 'Approved', 9, TRUE, TRUE, 1), ('APPL', 'Applied', 9, TRUE, TRUE, 1), ('VOID', 'Void', 9, TRUE, TRUE, 1),
    ('PEND', 'Pending', 10, TRUE, TRUE, 1), ('APPV', 'Approved', 10, TRUE, TRUE, 1), ('SENT', 'Sent', 10, TRUE, TRUE, 1), ('VOID', 'Void', 10, TRUE, TRUE, 1),
    ('ACT_', 'Active', 11, TRUE, TRUE, 1), ('INA_', 'Inactive', 11, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 12, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 12, TRUE, TRUE, 1), ('APPV', 'Approved', 12, TRUE, TRUE, 1), ('CANC', 'Cancelled', 12, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 13, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 13, TRUE, TRUE, 1), ('APPV', 'Approved', 13, TRUE, TRUE, 1),
    ('SENT', 'Sent to Vendor', 13, TRUE, TRUE, 1), ('PART', 'Partially Received', 13, TRUE, TRUE, 1), ('RCVD', 'Received', 13, TRUE, TRUE, 1), ('CLSD', 'Closed', 13, TRUE, TRUE, 1), ('CANC', 'Cancelled', 13, TRUE, TRUE, 1),
    ('PEND', 'Pending', 14, TRUE, TRUE, 1), ('RCVD', 'Received', 14, TRUE, TRUE, 1), ('PART', 'Partial', 14, TRUE, TRUE, 1), ('VOID', 'Void', 14, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 15, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 15, TRUE, TRUE, 1), ('APPV', 'Approved', 15, TRUE, TRUE, 1),
    ('PART', 'Partially Paid', 15, TRUE, TRUE, 1), ('PAID', 'Paid', 15, TRUE, TRUE, 1), ('ODUE', 'Overdue', 15, TRUE, TRUE, 1), ('VOID', 'Void', 15, TRUE, TRUE, 1),
    ('PEND', 'Pending', 16, TRUE, TRUE, 1), ('APPV', 'Approved', 16, TRUE, TRUE, 1), ('SENT', 'Sent', 16, TRUE, TRUE, 1), ('VOID', 'Void', 16, TRUE, TRUE, 1),
    ('DRFT', 'Draft', 17, TRUE, TRUE, 1), ('APPV', 'Approved', 17, TRUE, TRUE, 1), ('APPL', 'Applied', 17, TRUE, TRUE, 1), ('VOID', 'Void', 17, TRUE, TRUE, 1)
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- 6. lkp_crm_status ---------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_crm_status (
    crm_status_id           SERIAL       PRIMARY KEY,
    crm_status_code         VARCHAR(10)  NOT NULL,
    crm_status_name         VARCHAR(50)  NOT NULL,
    crm_status_record_type  INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    crm_status_is_active    BOOLEAN      NOT NULL DEFAULT TRUE,
    crm_status_is_system    BOOLEAN      NOT NULL DEFAULT FALSE,
    crm_status_created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    crm_status_created_by   INTEGER      NOT NULL REFERENCES employee(employee_id),
    crm_status_deleted_at   TIMESTAMP        NULL,
    crm_status_deleted_by   INTEGER          NULL REFERENCES employee(employee_id),
    crm_status_record_version INTEGER    NOT NULL DEFAULT 1,
    CONSTRAINT uq_crm_status_code_type UNIQUE (crm_status_code, crm_status_record_type),
    CONSTRAINT uq_crm_status_name_type UNIQUE (crm_status_name, crm_status_record_type)
);

INSERT INTO lkp_crm_status (crm_status_code, crm_status_name, crm_status_record_type, crm_status_is_active, crm_status_is_system, crm_status_created_by) VALUES
    ('LQUA', 'Lead Qualified',                       1, TRUE, TRUE, 1),
    ('LUNQ', 'Lead Unqualified',                     1, TRUE, TRUE, 1),
    ('PDIS', 'Prospect In Discussion',               2, TRUE, TRUE, 1),
    ('PNEG', 'Prospect In Negotiation',              2, TRUE, TRUE, 1),
    ('PPRP', 'Prospect Proposal',                    2, TRUE, TRUE, 1),
    ('PIDM', 'Prospect Identified Decision Makers',  2, TRUE, TRUE, 1),
    ('PPUR', 'Prospect Purchasing',                  2, TRUE, TRUE, 1),
    ('PCLL', 'Prospect Closed Lost',                 2, TRUE, TRUE, 1),
    ('CCLW', 'Customer Closed Won',                  3, TRUE, TRUE, 1),
    ('CCLL', 'Customer Closed Lost',                 3, TRUE, TRUE, 1),
    ('CREN', 'Customer Renewal',                     3, TRUE, TRUE, 1)
ON CONFLICT (crm_status_code, crm_status_record_type) DO NOTHING;

-- 7. lkp_customer_type ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_customer_type (
    customer_type_id    SERIAL        PRIMARY KEY,
    customer_type_name  VARCHAR(100)  NOT NULL,
    customer_type_code  VARCHAR(10)   NOT NULL,
    customer_type_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    customer_type_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    customer_type_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_type_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    customer_type_deleted_at  TIMESTAMP   NULL,
    customer_type_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    customer_type_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_customer_type_code UNIQUE (customer_type_code)
);

INSERT INTO lkp_customer_type (customer_type_name, customer_type_code, customer_type_is_active, customer_type_is_system, customer_type_created_by) VALUES
    ('Individual',          'INDV',  TRUE, TRUE, 1),
    ('Retail',              'RETL',  TRUE, TRUE, 1),
    ('Designer',            'DSGN',  TRUE, TRUE, 1),
    ('National Builder',    'NBLD',  TRUE, TRUE, 1),
    ('Custom Builder',      'CBLD',  TRUE, TRUE, 1),
    ('Regional Builder',    'RBLD',  TRUE, TRUE, 1),
    ('Multi-Family Builder','MFBLD', TRUE, TRUE, 1),
    ('Commercial Builder',  'COMBLD',TRUE, TRUE, 1),
    ('General Contractor',  'GCON',  TRUE, TRUE, 1)
ON CONFLICT (customer_type_code) DO NOTHING;

-- 8. lkp_customer_ar_status -------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_customer_ar_status (
    customer_ar_status_id    SERIAL       PRIMARY KEY,
    customer_ar_status_name  VARCHAR(50)  NOT NULL,
    customer_ar_status_code  VARCHAR(10)  NOT NULL,
    customer_ar_status_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    customer_ar_status_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    customer_ar_status_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    customer_ar_status_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    customer_ar_status_deleted_at  TIMESTAMP   NULL,
    customer_ar_status_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    customer_ar_status_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_customer_ar_status_code UNIQUE (customer_ar_status_code)
);

INSERT INTO lkp_customer_ar_status (customer_ar_status_name, customer_ar_status_code, customer_ar_status_is_active, customer_ar_status_is_system, customer_ar_status_created_by) VALUES
    ('Current',      'CURR', TRUE, TRUE, 1), ('Due Soon',     'DUSN', TRUE, TRUE, 1), ('Past Due',     'PDUE', TRUE, TRUE, 1),
    ('Delinquent',   'DLNQ', TRUE, TRUE, 1), ('Credit Hold',  'CRHD', TRUE, TRUE, 1), ('Collections',  'COLL', TRUE, TRUE, 1),
    ('Bad Debt',     'BDBT', TRUE, TRUE, 1)
ON CONFLICT (customer_ar_status_code) DO NOTHING;

-- 9. lkp_payment_terms ------------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_payment_terms (
    payment_terms_id    SERIAL       PRIMARY KEY,
    payment_terms_name  VARCHAR(50)  NOT NULL,
    payment_terms_code  VARCHAR(10)  NOT NULL,
    payment_terms_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    payment_terms_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    payment_terms_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_terms_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    payment_terms_deleted_at  TIMESTAMP   NULL,
    payment_terms_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    payment_terms_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_terms_code UNIQUE (payment_terms_code)
);

INSERT INTO lkp_payment_terms (payment_terms_name, payment_terms_code, payment_terms_is_active, payment_terms_is_system, payment_terms_created_by) VALUES
    ('Net 10',            'N10_', TRUE, TRUE, 1), ('Net 15',            'N15_', TRUE, TRUE, 1), ('Net 30',            'N30_', TRUE, TRUE, 1),
    ('Net 45',            'N45_', TRUE, TRUE, 1), ('Net 60',            'N60_', TRUE, TRUE, 1), ('Net 90',            'N90_', TRUE, TRUE, 1),
    ('Net 120',           'N120', TRUE, TRUE, 1), ('Cash on Receipt',   'COR_', TRUE, TRUE, 1), ('Cash on Delivery',  'COD_', TRUE, TRUE, 1),
    ('Due on Receipt',    'DOR_', TRUE, TRUE, 1), ('50% Deposit Net 30','D50N', TRUE, TRUE, 1)
ON CONFLICT (payment_terms_code) DO NOTHING;

-- 10. lkp_crm_lead_source ---------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_crm_lead_source (
    lead_source_id    SERIAL       PRIMARY KEY,
    lead_source_name  VARCHAR(50)  NOT NULL,
    lead_source_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    lead_source_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    lead_source_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lead_source_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    lead_source_deleted_at  TIMESTAMP   NULL,
    lead_source_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    lead_source_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_lead_source_name UNIQUE (lead_source_name)
);

INSERT INTO lkp_crm_lead_source (lead_source_name, lead_source_is_active, lead_source_is_system, lead_source_created_by) VALUES
    ('Web Search', TRUE, TRUE, 1), ('Facebook', TRUE, TRUE, 1), ('Instagram', TRUE, TRUE, 1), ('LinkedIn', TRUE, TRUE, 1),
    ('Trade Show', TRUE, TRUE, 1), ('Referral', TRUE, TRUE, 1), ('Email Campaign', TRUE, TRUE, 1)
ON CONFLICT (lead_source_name) DO NOTHING;

-- 11. lkp_contact_method ----------------------------------------------
CREATE TABLE IF NOT EXISTS lkp_contact_method (
    contact_method_id    SERIAL       PRIMARY KEY,
    contact_method_name  VARCHAR(50)  NOT NULL,
    contact_method_is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    contact_method_is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    contact_method_created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    contact_method_created_by  INTEGER NOT NULL REFERENCES employee(employee_id),
    contact_method_deleted_at  TIMESTAMP   NULL,
    contact_method_deleted_by  INTEGER     NULL REFERENCES employee(employee_id),
    contact_method_record_version INTEGER NOT NULL DEFAULT 1,
    CONSTRAINT uq_contact_method_name UNIQUE (contact_method_name)
);

INSERT INTO lkp_contact_method (contact_method_name, contact_method_is_active, contact_method_is_system, contact_method_created_by) VALUES
    ('Email', TRUE, TRUE, 1), ('Phone', TRUE, TRUE, 1), ('Text', TRUE, TRUE, 1), ('Postal Mail', TRUE, TRUE, 1)
ON CONFLICT (contact_method_name) DO NOTHING;

-- Indexes — active-record queries -------------------------------------
CREATE INDEX IF NOT EXISTS idx_currency_active ON lkp_currency (currency_is_active) WHERE currency_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_country_active ON lkp_country (country_is_active) WHERE country_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_state_active ON lkp_state (state_is_active) WHERE state_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_state_country ON lkp_state (state_country_id) WHERE state_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_record_type_active ON lkp_record_type (record_type_is_active) WHERE record_type_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_record_status_type ON lkp_record_status (record_status_record_type) WHERE record_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_crm_status_type ON lkp_crm_status (crm_status_record_type) WHERE crm_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_type_active ON lkp_customer_type (customer_type_is_active) WHERE customer_type_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_customer_ar_status_active ON lkp_customer_ar_status (customer_ar_status_is_active) WHERE customer_ar_status_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_payment_terms_active ON lkp_payment_terms (payment_terms_is_active) WHERE payment_terms_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_lead_source_active ON lkp_crm_lead_source (lead_source_is_active) WHERE lead_source_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_contact_method_active ON lkp_contact_method (contact_method_is_active) WHERE contact_method_deleted_at IS NULL;
