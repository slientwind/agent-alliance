CREATE TABLE web_search_integration (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 provider text NOT NULL CHECK(provider IN ('tavily','bocha')),
 endpoint text NOT NULL,
 encrypted_key bytea,
 enabled boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1 CHECK(revision>0),
 checked_at timestamptz,
 health text NOT NULL DEFAULT 'unchecked' CHECK(health IN ('unchecked','ready','unavailable')),
 CHECK(NOT enabled OR encrypted_key IS NOT NULL)
);
INSERT INTO web_search_integration(provider,endpoint) VALUES('tavily','https://api.tavily.com/search');
