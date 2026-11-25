CREATE TABLE model_providers (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 name text NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 120),
 kind text NOT NULL CHECK (kind IN ('bailian','ark','deepseek','openai','anthropic','gemini','vllm','ollama')),
 endpoint text NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 2048),
 encrypted_key bytea,
 enabled boolean NOT NULL DEFAULT true,
 revision integer NOT NULL DEFAULT 1 CHECK (revision>0),
 health text NOT NULL DEFAULT 'unchecked' CHECK (health IN ('unchecked','healthy','unavailable')),
 probed_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model_catalog (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 provider_id uuid NOT NULL REFERENCES model_providers,
 upstream_model text NOT NULL CHECK (length(btrim(upstream_model)) BETWEEN 1 AND 240),
 capabilities text[] NOT NULL CHECK (cardinality(capabilities)>0 AND capabilities <@
  ARRAY['text','tool_call','reasoning','vision','image','embedding']::text[]),
 context_tokens integer NOT NULL CHECK (context_tokens>=0),
 max_output_tokens integer NOT NULL CHECK (max_output_tokens>=0),
 rpm integer CHECK (rpm>0),
 tpm integer CHECK (tpm>0),
 enabled boolean NOT NULL DEFAULT true,
 UNIQUE (provider_id,upstream_model),
 CHECK (context_tokens=0 OR max_output_tokens<=context_tokens)
);

CREATE TABLE model_pricing (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 model_id uuid NOT NULL REFERENCES model_catalog,
 effective_at timestamptz NOT NULL,
 input_per_1m_micro_cny bigint NOT NULL CHECK (input_per_1m_micro_cny>=0),
 output_per_1m_micro_cny bigint NOT NULL CHECK (output_per_1m_micro_cny>=0),
 cached_per_1m_micro_cny bigint NOT NULL CHECK (cached_per_1m_micro_cny>=0),
 per_image_micro_cny bigint NOT NULL CHECK (per_image_micro_cny>=0),
 UNIQUE (model_id,effective_at)
);

CREATE FUNCTION retain_model_pricing() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'model price versions are immutable';
END $$;

CREATE TRIGGER model_pricing_immutable BEFORE UPDATE OR DELETE ON model_pricing
 FOR EACH ROW EXECUTE FUNCTION retain_model_pricing();

CREATE TABLE model_tier_routes (
 tier text NOT NULL CHECK (tier IN ('default','fast','reasoning','vision','image','embedding')),
 model_id uuid NOT NULL REFERENCES model_catalog,
 priority smallint NOT NULL CHECK (priority>=0),
 weight smallint NOT NULL DEFAULT 1 CHECK (weight>0),
 PRIMARY KEY (tier,model_id)
);

CREATE UNIQUE INDEX model_tier_one_primary ON model_tier_routes(tier) WHERE priority=0;
