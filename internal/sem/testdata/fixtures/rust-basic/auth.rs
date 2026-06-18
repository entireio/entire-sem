use std::collections::HashMap;

pub struct Token {
    pub value: String,
}

impl Token {
    pub fn validate(&self) -> bool {
        !self.value.is_empty()
    }
}

pub fn check_token(value: String) -> bool {
    let token = Token { value };
    token.validate()
}

pub fn index() -> HashMap<String, bool> {
    HashMap::new()
}
