-- The language an account is written to in.
--
-- A deployment has one locale and the people using it do not have one
-- language: a family planning one evening across two countries is the ordinary
-- case here, not the edge of it. The interface could already be read in three
-- languages, by whoever thought to add ?lang= to the address. The mail could
-- not — an invitation is composed on the server, which knew the deployment's
-- locale and nothing about the person it was writing to, so the first thing
-- somebody received was in whichever language the operator happened to set.
--
-- The admin who creates the account knows who they are inviting, so that is
-- where the choice is made.
--
-- Null means "whatever the deployment speaks", and is what every account that
-- existed before this migration gets. That is deliberately not a default
-- value: a default would freeze today's deployment language into each row, and
-- an operator who later changed SOIREE_LOCALE would find the accounts that
-- never chose anything still writing in the old one.
--
-- The check is on the shape of the tag and not on a list of languages. Which
-- languages exist is a fact about the templates compiled into the binary, and
-- the binary validates against that list; a list repeated here would mean a
-- migration every time a translation was added, and two places to disagree.
ALTER TABLE users
  ADD COLUMN language text
    CHECK (language IS NULL OR language ~ '^[a-z]{2,3}$');
