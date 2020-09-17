import React, {Component} from 'react';
import Header from "./Header";
import MainPane from "./MainPane";
import Footer from "./Footer";
import {BrowserRouter as Router, Route, Switch} from "react-router-dom";
import Services from "./Services";
import Filters from "./Filters";
import Rules from "./Rules";
import Config from "./Config";
import Upload from "./Upload";

class App extends Component {

    constructor(props) {
        super(props);
        this.state = {
            servicesWindowOpen: false,
            filterWindowOpen: false,
            rulesWindowOpen: false,
            configWindowOpen: false,
            uploadWindowOpen: false,
            configDone: false
        };

		fetch('/api/services')
		.then(response => {
			if( response.status === 503){
				this.setState({configWindowOpen: true});
			} else if (response.status === 200){
				this.setState({configDone: true});
			}
		});


    }

    render() {
        let modal;
        if (this.state.servicesWindowOpen) {
            modal = <Services onHide={() => this.setState({servicesWindowOpen: false})}/>;
        }
        if (this.state.filterWindowOpen) {
            modal = <Filters onHide={() => this.setState({filterWindowOpen: false})}/>;
        }
        if (this.state.rulesWindowOpen) {
            modal = <Rules onHide={() => this.setState({rulesWindowOpen: false})}/>;
        }
        if (this.state.configWindowOpen) {
            modal = <Config onHide={() => this.setState({configWindowOpen: false})}
						onDone={() => this.setState({configDone: true})} configDone={this.state.configDone} />;
        }
        if (this.state.uploadWindowOpen) {
            modal = <Upload onHide={() => this.setState({uploadWindowOpen: false}) }/>;
        }

        return (
            <div className="app">
                <Router>
                    <Header onOpenServices={() => this.setState({servicesWindowOpen: true})}
                            onOpenFilters={() => this.setState({filterWindowOpen: true})}
                            onOpenRules={() => this.setState({rulesWindowOpen: true})} 
                            onOpenConfig={() => this.setState({configWindowOpen: true})} 
                            onOpenUpload={() => this.setState({uploadWindowOpen: true})} 
							onConfigDone={this.state.configDone}
					/>
                    <Switch>
                        <Route path="/connections/:id" children={<MainPane/>}/>
                        <Route path="/" children={<MainPane/>}/>
                    </Switch>
                    {modal}
                    <Footer/>
                </Router>

            </div>
        );
    }
}

export default App;
